package core

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"time"
)

// When the app dies, the reason is written to a file of its own in crashes/, away from the logs
// (which get cleaned up), and the next start says so. A run that vanished without any reason
// (killed from outside, or the computer went down) is noticed through a marker file.

const runningMarker = "running"

var crashFile string

// SetupCrashReports directs what a crash prints to a new file in dataDir/crashes and returns a
// function to call on a clean exit. The newest reports are kept.
func SetupCrashReports(dataDir string) func() {
	dir := filepath.Join(dataDir, "crashes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return func() {}
	}
	pruneCrashReports(dir, 20)
	path := filepath.Join(dir, time.Now().Format("20060102-150405")+".txt")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return func() {}
	}
	crashFile = path
	debug.SetCrashOutput(f, debug.CrashOptions{})
	return func() {
		f.Close()
		if st, err := os.Stat(path); err == nil && st.Size() == 0 {
			os.Remove(path)
		}
		crashFile = ""
	}
}

func pruneCrashReports(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".txt") {
			if st, err := e.Info(); err == nil && st.Size() == 0 { // a run that got no further than starting
				os.Remove(filepath.Join(dir, e.Name()))
				continue
			}
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for i := 0; i < len(names)-keep; i++ {
		os.Remove(filepath.Join(dir, names[i]))
	}
}

func (m *Manager) noteUncleanExit() {
	marker := filepath.Join(m.dataDir, runningMarker)
	if _, err := os.Stat(marker); err == nil {
		dir := filepath.Join(m.dataDir, "crashes")
		entries, _ := os.ReadDir(dir)
		var newest string
		var at time.Time
		for _, e := range entries {
			if st, err := e.Info(); err == nil && st.Size() > 0 && st.ModTime().After(at) && strings.HasSuffix(e.Name(), ".txt") {
				newest, at = filepath.Join(dir, e.Name()), st.ModTime()
			}
		}
		var last string
		if st, err := os.Stat(marker); err == nil {
			last = st.ModTime().Format("15:04 on 2 January")
			if newest != "" && at.Before(st.ModTime()) {
				newest = "" // a report from an earlier crash, not the last run's
			}
		}
		if newest != "" {
			m.lastCrash = fmt.Sprintf("The app crashed last time. The report was saved in %s", newest)
		} else {
			m.lastCrash = fmt.Sprintf("The app didn't close properly last time (it was started at %s). It may have been stopped from outside, or the computer shut down.", last)
		}
		Logf("%s", m.lastCrash)
	}
	os.WriteFile(marker, []byte(time.Now().Format(time.RFC3339)), 0o644)
}

// LastCrash says what ended the previous run, when it didn't end by being closed ("": it did).
func (m *Manager) LastCrash() string { return m.lastCrash }

// logHealth notes how much the app is holding, for reading a log after something went wrong.
func logHealth() {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	debugf("health: %d goroutines, %d MiB heap, %d MiB from the system, GC %d", runtime.NumGoroutine(), ms.HeapAlloc>>20, ms.Sys>>20, ms.NumGC)
}
