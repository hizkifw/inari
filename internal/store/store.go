// Package store remembers which kon session each conversation belongs to, in
// one plain JSON file, so a restart picks up where each chat left off.
package store

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// Binding is a conversation's kon session and the directory it works in.
// kon sessions belong to their directory, so a binding whose directory no
// longer matches the config is stale.
type Binding struct {
	SessionID string `json:"session_id"`
	CWD       string `json:"cwd"`
}

// Store is the conversation-to-session map, written through on every change.
type Store struct {
	path string
	mu   sync.Mutex
	m    map[string]Binding
}

// Open loads the store at path, starting empty when there is none.
func Open(path string) (*Store, error) {
	s := &Store{path: path, m: map[string]Binding{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.m); err != nil {
		return nil, err
	}
	return s, nil
}

// Get returns conv's binding.
func (s *Store) Get(conv string) (Binding, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[conv]
	return b, ok
}

// Set binds conv to b.
func (s *Store) Set(conv string, b Binding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[conv] = b
	return s.save()
}

// Delete forgets conv's binding.
func (s *Store) Delete(conv string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, conv)
	return s.save()
}

// save replaces the file atomically, so a crash mid-write leaves the old map.
func (s *Store) save() error {
	data, err := json.MarshalIndent(s.m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
