// Package state persists desired-state for instances: whether each is
// enabled for boot autostart and whether it should currently be running.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Instance is the persisted desired state of one instance.
type Instance struct {
	// Enabled: start automatically when the daemon starts (boot autostart).
	Enabled bool `json:"enabled"`
	// DesiredRun: the daemon works toward this running state.
	DesiredRun bool `json:"desired_run"`
}

// Store persists instance state as JSON. Safe for concurrent use.
type Store struct {
	mu   sync.Mutex
	path string
	m    map[string]Instance
}

// Load reads state from path (empty map if missing).
func Load(path string) (*Store, error) {
	s := &Store{path: path, m: map[string]Instance{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.m); err != nil {
		return nil, fmt.Errorf("parse %s: %v", path, err)
	}
	return s, nil
}

// Get returns the state of one instance (zero value if unknown).
func (s *Store) Get(name string) Instance {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[name]
}

// Has reports whether an instance has a persisted record.
func (s *Store) Has(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[name]
	return ok
}

// Set stores the state of one instance and persists atomically.
func (s *Store) Set(name string, in Instance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[name] = in
	return s.saveLocked()
}

// Update applies fn to one instance's state and persists atomically.
func (s *Store) Update(name string, fn func(*Instance)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := s.m[name]
	fn(&in)
	s.m[name] = in
	return s.saveLocked()
}

// Remove drops one instance's state.
func (s *Store) Remove(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, name)
	return s.saveLocked()
}

// Names returns all known instance names, sorted.
func (s *Store) Names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.m))
	for k := range s.m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// saveLocked writes the state file via tmp+rename. Caller holds s.mu.
func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.m, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
