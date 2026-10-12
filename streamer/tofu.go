package streamer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// TOFUStore persists peer identity fingerprints (first-trust, then pin).
type TOFUStore struct {
	mu    sync.Mutex
	path  string
	Pins  map[string]string // key → fingerprint hex
	dirty bool
}

// TOFUPinKey builds a stable store key from signaling URL + peer slot.
func TOFUPinKey(signalURL, peer string) string {
	u := strings.TrimRight(strings.TrimSpace(signalURL), "/")
	p := strings.TrimSpace(peer)
	if p == "" {
		p = "default"
	}
	return u + "|" + p
}

// LoadTOFUStore reads JSON pins from path (empty file → empty store).
func LoadTOFUStore(path string) (*TOFUStore, error) {
	if path == "" {
		var err error
		path, err = DefaultTOFUPath()
		if err != nil {
			return nil, err
		}
	}
	s := &TOFUStore{path: path, Pins: make(map[string]string)}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if len(b) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(b, &s.Pins); err != nil {
		return nil, fmt.Errorf("tofu: parse %s: %w", path, err)
	}
	if s.Pins == nil {
		s.Pins = make(map[string]string)
	}
	return s, nil
}

// CheckOrPin pins fp on first sight; returns error on mismatch with stored pin.
func (s *TOFUStore) CheckOrPin(key, fp string) error {
	if s == nil || key == "" || fp == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, ok := s.Pins[key]
	if !ok || prev == "" {
		s.Pins[key] = fp
		s.dirty = true
		return nil
	}
	if prev != fp {
		return fmt.Errorf("tofu pin mismatch for %s: got %s want %s (delete pin or use -tofu=off)", key, fp, prev)
	}
	return nil
}

// Save writes pins if dirty.
func (s *TOFUStore) Save() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.Pins, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

// Path returns the on-disk path.
func (s *TOFUStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}
