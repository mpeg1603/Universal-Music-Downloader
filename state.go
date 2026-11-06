package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Per-track results, kept as an append-only JSON-lines file so an interrupted run loses
// nothing and the next run picks up where it stopped.

type Record struct {
	Key             string  `json:"key"`
	Status          string  `json:"status"`         // downloaded | failed | error (retried automatically) | cancelled
	Path            string  `json:"path,omitempty"` // absolute
	Quality         Quality `json:"quality"`
	LibPath         string  `json:"library_path,omitempty"` // the library copy this download improves on
	LibQuality      Quality `json:"library_quality"`
	Source          string  `json:"source,omitempty"`
	SourceURL       string  `json:"source_url,omitempty"`
	SourceTitle     string  `json:"source_title,omitempty"`
	SourceArtist    string  `json:"source_artist,omitempty"`
	SourceArtistURL string  `json:"source_artist_url,omitempty"`
	SourceAlbum     string  `json:"source_album,omitempty"` // for torrents: the torrent's name
	Score           float64 `json:"score,omitempty"`
	Reason          string  `json:"reason,omitempty"`
	Attempts        int     `json:"attempts"`
	Updated         string  `json:"updated"`
}

type State struct {
	mu   sync.Mutex
	path string
	recs map[string]*Record
	f    *os.File
}

func openState(path string) (*State, error) {
	s := &State{path: path, recs: map[string]*Record{}}
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for sc.Scan() {
			var r Record
			if json.Unmarshal(sc.Bytes(), &r) == nil && r.Key != "" {
				s.recs[r.Key] = &r
			}
		}
		f.Close()
	}
	// Rewrite compactly (one line per track), then append from here on.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return nil, err
	}
	w := bufio.NewWriter(f)
	for _, r := range s.recs {
		line, _ := json.Marshal(r)
		w.Write(append(line, '\n'))
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	if s.f, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *State) get(key string) *Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.recs[key]; r != nil {
		c := *r
		return &c
	}
	return nil
}

func (s *State) all() map[string]Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Record, len(s.recs))
	for k, r := range s.recs {
		out[k] = *r
	}
	return out
}

func (s *State) ownerOf(path string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, r := range s.recs {
		if r.Status == "downloaded" && r.Path == path {
			return k
		}
	}
	return ""
}

func (s *State) put(r Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old := s.recs[r.Key]; old != nil {
		r.Attempts = old.Attempts
	}
	r.Attempts++
	r.Updated = time.Now().UTC().Format(time.RFC3339)
	s.recs[r.Key] = &r
	line, _ := json.Marshal(r)
	s.f.Write(append(line, '\n'))
}

func (s *State) markFailed(key, reason string, transient bool) {
	status := "failed"
	if transient {
		status = "error"
	}
	s.put(Record{Key: key, Status: status, Reason: reason})
}

func (s *State) markCancelled(key string) {
	s.put(Record{Key: key, Status: "cancelled", Reason: "cancelled by you"})
}

func (s *State) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.f.Close()
}
