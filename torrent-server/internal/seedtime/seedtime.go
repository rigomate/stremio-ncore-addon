// Package seedtime persists locally observed seeding time, excluding server downtime.
package seedtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Record struct {
	Source  string  `json:"source"`
	Seconds float64 `json:"seconds"`
	last    time.Time
}

type Store struct {
	mu      sync.Mutex
	path    string
	records map[string]Record
}

func Open(path string) (*Store, error) {
	s := &Store{path: path, records: make(map[string]Record)}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(data, &s.records); err != nil {
		return nil, err
	}
	if s.records == nil {
		s.records = make(map[string]Record)
	}
	return s, nil
}

func (s *Store) save() error {
	data, err := json.Marshal(s.records)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	if err = os.WriteFile(s.path+".tmp", data, 0600); err != nil {
		return err
	}
	return os.Rename(s.path+".tmp", s.path)
}

func (s *Store) Register(hash, source string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.records[hash]
	// Preserve BitHUmen protection when the same hash is requested through another source.
	if r.Source == "bithumen" || source != "bithumen" {
		return nil
	}
	r.Source = source
	s.records[hash] = r
	return s.save()
}

func (s *Store) Get(hash string) Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.records[hash]
}

// Observe counts only consecutive sharing observations at one-minute intervals.
// Missing torrents, unavailable pieces, restarts and long suspension gaps earn no credit.
func (s *Store) Observe(now time.Time, seeding map[string]bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for hash, r := range s.records {
		if seeding[hash] {
			elapsed := now.Sub(r.last)
			if !r.last.IsZero() && elapsed > 0 && elapsed <= 90*time.Second {
				r.Seconds += elapsed.Seconds()
			}
			r.last = now
		} else {
			r.last = time.Time{}
		}
		s.records[hash] = r
	}
	return s.save()
}

func (s *Store) Remove(hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, hash)
	return s.save()
}
