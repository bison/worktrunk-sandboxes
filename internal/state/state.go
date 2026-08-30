// Package state persists active grants. One JSON file guarded by a lock
// directory: timer sweeps run concurrently with interactive commands, and
// losing a row would leak a grant.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Grant is one active grant row. The undo spec is recorded at grant time
// ("service:NAME", "custom:HOST:ENV:PLACEHOLDER", "netrule:ID[,ID...]") so
// revocation never depends on the config entry still existing.
type Grant struct {
	Expiry  int64  `json:"expiry"` // unix epoch seconds; 0 = no expiry
	Sandbox string `json:"sandbox"`
	Branch  string `json:"branch"`
	Repo    string `json:"repo"` // main repo path; markers need it
	Name    string `json:"name"`
	Undo    string `json:"undo"`
}

// Expired reports whether the grant's deadline has passed at now.
func (g Grant) Expired(now time.Time) bool {
	return g.Expiry != 0 && g.Expiry <= now.Unix()
}

// Store is the on-disk grants state.
type Store struct {
	Dir string
}

// DefaultDir returns the state directory, honoring XDG_STATE_HOME.
func DefaultDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "wt-sbx")
}

func New(dir string) *Store { return &Store{Dir: dir} }

func (s *Store) file() string { return filepath.Join(s.Dir, "grants.json") }
func (s *Store) lock() string { return filepath.Join(s.Dir, ".lock") }

// Load reads the current rows without taking the lock (reads are fine on
// a stale snapshot; every mutation goes through Update).
func (s *Store) Load() ([]Grant, error) {
	data, err := os.ReadFile(s.file())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		Grants []Grant `json:"grants"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", s.file(), err)
	}
	return doc.Grants, nil
}

// Update applies fn to the rows under the lock and writes the result back
// atomically (temp file + rename).
func (s *Store) Update(fn func([]Grant) []Grant) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	if err := s.acquire(); err != nil {
		return err
	}
	defer s.release()

	rows, err := s.Load()
	if err != nil {
		return err
	}
	rows = fn(rows)

	doc := struct {
		Grants []Grant `json:"grants"`
	}{Grants: rows}
	if doc.Grants == nil {
		doc.Grants = []Grant{}
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.file() + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.file())
}

func (s *Store) acquire() error {
	for tries := 0; ; tries++ {
		err := os.Mkdir(s.lock(), 0o700)
		if err == nil {
			return nil
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		if tries >= 50 {
			return fmt.Errorf("state locked; stale %s?", s.lock())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (s *Store) release() {
	_ = os.Remove(s.lock())
}
