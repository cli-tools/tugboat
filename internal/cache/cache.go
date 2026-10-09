// Package cache stores disposable discovery data outside repository worktrees.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type Store struct{ Dir string }

func Default() *Store {
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil
	}
	return &Store{Dir: filepath.Join(dir, "tugboat", "discovery-v1")}
}

func Key(parts ...string) string {
	data, _ := json.Marshal(parts)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

type entry struct {
	Version int             `json:"version"`
	Saved   time.Time       `json:"saved"`
	Value   json.RawMessage `json:"value"`
}

// Read treats missing, expired and malformed data as a cache miss.
func (s *Store) Read(key string, maxAge time.Duration, value any) bool {
	if s == nil || s.Dir == "" {
		return false
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, Key(key)+".json"))
	if err != nil {
		return false
	}
	var e entry
	if json.Unmarshal(data, &e) != nil || e.Version != 1 {
		return false
	}
	age := time.Since(e.Saved)
	if age < 0 || age > maxAge {
		return false
	}
	return json.Unmarshal(e.Value, value) == nil
}

// Write is best effort: cache failures never prevent repository discovery.
// Each key is replaced atomically, so concurrent processes cannot truncate it.
func (s *Store) Write(key string, value any) {
	if s == nil || s.Dir == "" {
		return
	}
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	data, err = json.Marshal(entry{Version: 1, Saved: time.Now(), Value: data})
	if err != nil || os.MkdirAll(s.Dir, 0700) != nil {
		return
	}
	f, err := os.CreateTemp(s.Dir, ".write-")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr == nil && closeErr == nil {
		_ = os.Rename(f.Name(), filepath.Join(s.Dir, Key(key)+".json"))
	}
}
