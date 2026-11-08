package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUpgradeLibrary(t *testing.T) {
	tools := NeedFFmpeg(t)
	lib := t.TempDir()
	dir := filepath.Join(lib, "Daft Punk", "Discovery")
	os.MkdirAll(dir, 0o755)
	sine(t, filepath.Join(dir, "03 - Digital Love.mp3"), 4)
	a := GetLucky()
	a.Key = "a"
	m := TestManager(t, tools, []Source{&FakeSource{SrcName: "youtube", T: t, Results: map[string][]Candidate{}}}, TestLibrary(a),
		func(c *Config) { c.LibraryDirs, c.Threshold = []string{lib}, "any" })
	counts := m.UpgradeCounts()
	if counts["lossless"] != 1 || counts["any"] != 0 {
		t.Fatalf("%v", counts)
	}
	name, n, err := m.UpgradeLibrary("lossless")
	if err != nil || n != 1 || name != "Upgrade: below lossless only" {
		t.Fatalf("%q %d %v", name, n, err)
	}
	// Your threshold ("any") would count the copy as good enough; the upgrade asks for more.
	m.reevaluateLibrary()
	q := m.Queue(QueueQuery{Filter: "upgrading"})
	if len(q) != 1 || q[0].State != JobQueued || !q[0].NeedBetter || q[0].Track.Title != "Digital Love" {
		t.Fatalf("%+v", q)
	}
	if !m.RemoveAddition(name) {
		t.Fatal("not removed")
	}
	WaitFor(t, func() bool { return len(m.Queue(QueueQuery{Filter: "upgrading"})) == 0 })
}
