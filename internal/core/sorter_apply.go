package core

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Carrying out a plan, and undoing it. The rules:
//   - never overwrite: a move whose target already exists is skipped and reported;
//   - a file that changed after planning (size or modification time) is skipped;
//   - every completed move is appended to journal.jsonl right away, so undo works even after a
//     crash, and applying again simply continues where it stopped;
//   - nothing is ever deleted. Moves to another disk copy, check the size, then remove the original.

const (
	sortJournal    = "journal.jsonl"
	sortFinished   = "finished" // there once applying has gone through to the end
	sortReportName = "duplicates_report.txt"
)

type sortJournalEntry struct {
	Op   string `json:"op"` // move, copy, rmdir or report
	Src  string `json:"src,omitempty"`
	Dst  string `json:"dst,omitempty"`
	Path string `json:"path,omitempty"`
	Size int64  `json:"size,omitempty"`
	Prev *int64 `json:"previous_size,omitempty"` // the report's size before, nil if it's new
}

type sortJournalFile struct {
	f *os.File
	w *bufio.Writer
	n int
}

func openSortJournal(path string) (*sortJournalFile, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &sortJournalFile{f: f, w: bufio.NewWriter(f)}, nil
}

func (j *sortJournalFile) log(e sortJournalEntry) {
	data, _ := json.Marshal(e)
	j.w.Write(data)
	j.w.WriteByte('\n')
	j.w.Flush()
	if j.n++; j.n >= 100 {
		j.n = 0
		j.f.Sync()
	}
}

func (j *sortJournalFile) close() {
	j.w.Flush()
	j.f.Sync()
	j.f.Close()
}

func readSortJournal(path string) []sortJournalEntry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []sortJournalEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var e sortJournalEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

func sortExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// sortApplyOne carries out one move and says how it went: "done", "already done", or why not.
func sortApplyOne(m *SortMove, copyMode bool, j *sortJournalFile) string {
	srcExists := sortExists(m.Src)
	if st, err := os.Lstat(m.Dst); err == nil {
		if st.Size() == m.Size && (copyMode || !srcExists) {
			return "already done"
		}
		same := false
		if srcExists {
			if si, err := os.Stat(m.Src); err == nil {
				same = os.SameFile(si, st) // a rename that only changes case is fine
			}
		}
		if !same {
			return "target already exists"
		}
	}
	if !srcExists {
		return "file no longer exists"
	}
	st, err := os.Stat(m.Src)
	if err != nil {
		return "error: " + err.Error()
	}
	if st.Size() != m.Size || st.ModTime().UnixNano() != m.Mtime {
		return "file changed since planning"
	}
	if err := os.MkdirAll(filepath.Dir(m.Dst), 0o755); err != nil {
		return "error: " + err.Error()
	}
	op := "move"
	if copyMode {
		op = "copy"
		err = sortCopy(m.Src, m.Dst)
	} else {
		err = sortMove(m.Src, m.Dst)
	}
	if err != nil {
		return "error: " + err.Error()
	}
	j.log(sortJournalEntry{Op: op, Src: m.Src, Dst: m.Dst, Size: m.Size})
	return "done"
}

func sortMove(src, dst string) error {
	if sortExists(dst) { // the same file under a different case, on a case-insensitive disk
		tmp := dst + ".umd-sort-tmp"
		if err := os.Rename(src, tmp); err != nil {
			return err
		}
		return os.Rename(tmp, dst)
	}
	err := os.Rename(src, dst)
	if err == nil || !isCrossDevice(err) {
		return err
	}
	if err := sortCopy(src, dst); err != nil { // another disk: copy, check, then remove the original
		return err
	}
	return os.Remove(src)
}

func sortCopy(src, dst string) (err error) {
	partial := dst + ".part"
	defer func() {
		if err != nil {
			os.Remove(partial)
		}
	}()
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(partial, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	si, err := os.Stat(src)
	if err != nil {
		return err
	}
	pi, err := os.Stat(partial)
	if err != nil {
		return err
	}
	if si.Size() != pi.Size() {
		return errors.New("the size differs after copying")
	}
	os.Chtimes(partial, si.ModTime(), si.ModTime())
	return os.Rename(partial, dst)
}

func sortNearestDir(p string) string {
	for !sortExists(p) && filepath.Dir(p) != p {
		p = filepath.Dir(p)
	}
	return p
}

// sortFreeSpaceProblems compares what will really be written (copies, moves to another disk)
// with the free space there.
func sortFreeSpaceProblems(p *sortPlan) []string {
	needed := map[string]int64{}
	where := map[string]string{}
	for i := range p.Moves {
		m := &p.Moves[i]
		if !sortExists(m.Src) || sortExists(m.Dst) {
			continue
		}
		target := sortNearestDir(filepath.Dir(m.Dst))
		dev := deviceOf(target)
		if p.Options.Copy || deviceOf(m.Src) != dev {
			needed[dev] += m.Size
			where[dev] = target
		}
	}
	var out []string
	for dev, size := range needed {
		if free := diskFree(where[dev]); free >= 0 && size+256<<20 > free {
			out = append(out, fmt.Sprintf("%s: needs %s, has %s free", where[dev], FmtBytes(size), FmtBytes(free)))
		}
	}
	return out
}

func sortRemoveEmptyDirs(dirs map[string]bool, roots []string, j *sortJournalFile) {
	list := make([]string, 0, len(dirs))
	for d := range dirs {
		list = append(list, d)
	}
	sort.Slice(list, func(a, b int) bool { return len(list[a]) > len(list[b]) })
	inside := func(d string) bool {
		for _, r := range roots {
			if d == r {
				return false
			}
		}
		for _, r := range roots {
			if strings.HasPrefix(d, r+string(filepath.Separator)) {
				return true
			}
		}
		return false
	}
	for _, d := range list {
		for inside(d) {
			if os.Remove(d) != nil {
				break
			}
			if j != nil {
				j.log(sortJournalEntry{Op: "rmdir", Path: d})
			}
			d = filepath.Dir(d)
		}
	}
}

// SortResult says how applying or undoing went.
type SortResult struct {
	Done, AlreadyDone int
	Problems          []string // the first few
	ProblemCount      int
	Stopped           bool
	ReportPath        string
}

// sortApply carries out a plan; moved says which files went where.
func sortApply(ctx context.Context, p *sortPlan, dir string, progress func(string, int, int)) (SortResult, map[string]string, error) {
	var res SortResult
	if short := sortFreeSpaceProblems(p); len(short) > 0 {
		return res, nil, fmt.Errorf("not enough free space: %s", strings.Join(short, "; "))
	}
	j, err := openSortJournal(filepath.Join(dir, sortJournal))
	if err != nil {
		return res, nil, err
	}
	defer j.close()
	problems := map[string]string{}
	emptied := map[string]bool{}
	moved := map[string]string{}
	var partial sortPartialCheck
	verb := "Moving files"
	if p.Options.Copy {
		verb = "Copying files"
	}
	for i := range p.Moves {
		if ctx.Err() != nil {
			res.Stopped = true
			break
		}
		m := &p.Moves[i]
		status := "still being downloaded"
		if !partial.unfinished(m.Src) {
			status = sortApplyOne(m, p.Options.Copy, j)
		}
		switch status {
		case "done", "already done":
			if status == "done" {
				res.Done++
			} else {
				res.AlreadyDone++
			}
			if !p.Options.Copy { // a copy leaves the original where the app knows it to be
				moved[m.Src] = m.Dst
				emptied[filepath.Dir(m.Src)] = true
			}
		default:
			problems[m.Src] = status
		}
		if i%25 == 0 {
			progress(verb, i, len(p.Moves))
		}
	}
	progress(verb, len(p.Moves), len(p.Moves))
	if !res.Stopped {
		if len(p.Duplicates) > 0 {
			folder := p.Options.DuplicatesDir
			if err := os.MkdirAll(folder, 0o755); err == nil {
				path := filepath.Join(folder, sortReportName)
				var prev *int64
				if st, err := os.Stat(path); err == nil {
					n := st.Size()
					prev = &n
				}
				if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
					if prev != nil && *prev > 0 {
						f.WriteString("\n\n" + strings.Repeat("=", 100) + "\n\n")
					}
					f.WriteString(p.duplicatesReport(problems))
					f.Close()
					j.log(sortJournalEntry{Op: "report", Path: path, Prev: prev})
					res.ReportPath = path
				}
			}
		}
		if !p.Options.Copy {
			sortRemoveEmptyDirs(emptied, p.Options.Sources, j)
		}
		os.WriteFile(filepath.Join(dir, sortFinished), nil, 0o644)
	}
	res.ProblemCount = len(problems)
	if len(problems) > 0 {
		var b strings.Builder
		keys := make([]string, 0, len(problems))
		for k := range problems {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "%s\t%s\n", problems[k], k)
			if len(res.Problems) < 20 {
				res.Problems = append(res.Problems, problems[k]+": "+k)
			}
		}
		os.WriteFile(filepath.Join(dir, "apply_problems.txt"), []byte(b.String()), 0o644)
	}
	return res, moved, nil
}

// sortUndo puts back what a plan did, last things first; moved says which files went where.
func sortUndo(ctx context.Context, p *sortPlan, dir string, progress func(string, int, int)) (SortResult, map[string]string, error) {
	var res SortResult
	path := filepath.Join(dir, sortJournal)
	entries := readSortJournal(path)
	files := 0
	for _, e := range entries {
		if e.Op == "move" || e.Op == "copy" {
			files++
		}
	}
	if files == 0 {
		return res, nil, errors.New("there's nothing to undo")
	}
	touched := map[string]bool{}
	moved := map[string]string{}
	fail := func(s string) {
		res.ProblemCount++
		if len(res.Problems) < 20 {
			res.Problems = append(res.Problems, s)
		}
	}
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		switch e.Op {
		case "rmdir":
			os.MkdirAll(e.Path, 0o755)
		case "report":
			if e.Prev == nil {
				if os.Remove(e.Path) == nil {
					touched[filepath.Dir(e.Path)] = true
				}
			} else {
				os.Truncate(e.Path, *e.Prev)
			}
		case "move":
			if !sortExists(e.Dst) {
				fail("missing, can't put it back: " + e.Dst)
				continue
			}
			if st, err := os.Lstat(e.Src); err == nil {
				if di, err2 := os.Stat(e.Dst); err2 != nil || !os.SameFile(st, di) {
					fail("something else is in its place now: " + e.Src)
					continue
				}
			}
			if err := os.MkdirAll(filepath.Dir(e.Src), 0o755); err != nil {
				fail(err.Error())
				continue
			}
			if err := sortMove(e.Dst, e.Src); err != nil {
				fail(err.Error() + ": " + e.Dst)
				continue
			}
			res.Done++
			moved[e.Dst] = e.Src
			touched[filepath.Dir(e.Dst)] = true
		case "copy":
			if st, err := os.Lstat(e.Dst); err == nil && st.Size() == e.Size {
				if os.Remove(e.Dst) == nil {
					res.Done++
					touched[filepath.Dir(e.Dst)] = true
				}
			}
		}
		if i%25 == 0 {
			progress("Putting things back", len(entries)-i, len(entries))
		}
	}
	sortRemoveEmptyDirs(touched, []string{p.Options.Dest, p.Options.DuplicatesDir}, nil)
	for _, root := range []string{p.Options.DuplicatesDir, p.Options.Dest} {
		if !ContainsString(p.Options.Sources, root) {
			os.Remove(root) // only works if it's empty
		}
	}
	os.Rename(path, filepath.Join(dir, "journal.undone-"+time.Now().Format("20060102-150405")+".jsonl"))
	os.Remove(filepath.Join(dir, sortFinished))
	return res, moved, nil
}
