package core

import (
	"sync"
	"testing"
	"time"
)

func TestWatch(t *testing.T) {
	tools := NeedFFmpeg(t)
	tr := GetLucky()
	tr.Key = "a"
	m := TestManager(t, tools, nil, TestLibrary(tr), nil)
	var mu sync.Mutex
	got := map[EventKind]int{}
	stop := m.Watch(func(k EventKind) {
		mu.Lock()
		got[k]++
		mu.Unlock()
	})
	defer stop()
	seen := func(k EventKind) bool {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			n := got[k]
			mu.Unlock()
			if n > 0 {
				return true
			}
			time.Sleep(20 * time.Millisecond)
		}
		return false
	}
	if err := m.UpdateConfig(func(c *Config) { c.MaxDownloads = 3 }); err != nil {
		t.Fatal(err)
	}
	if !seen(EventConfig) {
		t.Error("no config event")
	}
	m.wake()
	if !seen(EventQueue) {
		t.Error("no queue event")
	}
	Logf("a line")
	if !seen(EventLog) {
		t.Error("no log event")
	}
	stop()
	mu.Lock()
	got = map[EventKind]int{}
	mu.Unlock()
	m.UpdateConfig(func(c *Config) { c.MaxDownloads = 4 })
	time.Sleep(600 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 0 {
		t.Errorf("events after stop: %v", got)
	}
}
