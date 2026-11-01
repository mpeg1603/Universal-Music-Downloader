package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Panel lines must never be wider than the terminal: a wrapped line breaks the redraw.
func TestPanelFitsTerminal(t *testing.T) {
	interpol := func(title string) *Track { return &Track{Title: title, Artists: []string{"Interpol"}} }
	board := &Board{}
	add := func(tr *Track, stage, note string, done, total int64) {
		task := &Task{Track: tr, started: time.Now()}
		task.setStage(stage)
		if total > 0 {
			task.setProgress(done, total)
		}
		task.setNote(note)
		board.add(task)
	}
	add(interpol("PDA"), "downloading", "torrent 2.0 MiB/s · 14 peers (3 seeding)", 41<<20, 86<<20)
	add(interpol("Stella Was a Diver and She Was Always Down"), "downloading", "torrent 2.0 MiB/s · 14 peers (3 seeding)", 12<<20, 95<<20)
	// The same file wanted by the album and its anniversary edition.
	add(&Track{Title: "Obstacle 1", Artists: []string{"Interpol"}, Album: "Turn On The Bright Lights"}, "downloading", "", 50<<20, 100<<20)
	add(&Track{Title: "Obstacle 1", Artists: []string{"Interpol"}, Album: "Turn On The Bright Lights: The Tenth Anniversary Edition"}, "downloading", "", 50<<20, 100<<20)
	for i, task := range board.tasks[:4] {
		task.share("t1", fmt.Sprint(min(i, 2)), 2<<20)
	}
	add(interpol("NYC"), "converting", "", 0, 0)
	add(interpol("Roland (First Demo)"), "searching Torrent", "", 0, 0)
	add(&Track{Title: "Архизадача", Artists: []string{"4 Позиции Бруно"}}, "queued behind other torrents", "", 0, 0)
	add(&Track{Title: "Lost Souls/Eelings", Artists: []string{"TR/ST"}}, "looking for peers", "0 peers", 0, 0)
	color = true
	defer func() { color = false }()
	for _, size := range [][2]int{{140, 40}, {80, 24}, {50, 8}} {
		u := &UI{board: board, total: 46, finished: 12, counts: map[string]int{"downloaded": 9, "failed": 3},
			started: time.Now().Add(-3 * time.Minute), diskPath: t.TempDir()}
		lines := u.panel(size[0], size[1])
		if len(lines) > size[1]-1 {
			t.Errorf("%dx%d: %d lines", size[0], size[1], len(lines))
		}
		for _, l := range lines {
			if w := displayWidth(l); w > size[0]-1 {
				t.Errorf("%dx%d: line %d columns wide: %q", size[0], size[1], w, l)
			}
		}
		t.Logf("%dx%d:\n%s", size[0], size[1], reANSI.ReplaceAllString(strings.Join(lines, "\n"), ""))
	}
	u := &UI{board: board, total: 46, counts: map[string]int{}, diskPath: t.TempDir()}
	overall := reANSI.ReplaceAllString(u.overallLine(board.snapshot(), 200), "")
	// One torrent at 2 MiB/s; 45+83+50 MiB left in its three distinct files.
	if !strings.Contains(overall, "⇣ 2.0 MiB/s · 178.0 MiB to go (~1m)") {
		t.Errorf("overall: %s", overall)
	}
}
