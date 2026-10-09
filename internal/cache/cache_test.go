package cache

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStoreMissesAndAtomicWrites(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	var value string
	if s.Read("missing", time.Hour, &value) {
		t.Fatal("missing entry was a hit")
	}
	s.Write("key", "value")
	if !s.Read("key", time.Hour, &value) || value != "value" {
		t.Fatal("round trip failed")
	}
	info, err := os.Stat(filepath.Join(s.Dir, Key("key")+".json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("cache permissions: %v, %v", info, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.Write("key", "complete") }()
	}
	wg.Wait()
	if !s.Read("key", time.Hour, &value) || value != "complete" {
		t.Fatal("concurrent write corrupted entry")
	}
	for _, data := range []string{
		`{`,
		`{"version":2,"saved":"2099-01-01T00:00:00Z","value":"bad"}`,
		`{"version":1,"saved":"2000-01-01T00:00:00Z","value":"expired"}`,
		`{"version":1,"saved":"2099-01-01T00:00:00Z","value":"future"}`,
	} {
		if err := os.WriteFile(filepath.Join(s.Dir, Key("key")+".json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if s.Read("key", time.Hour, &value) {
			t.Fatalf("accepted invalid entry: %s", data)
		}
	}
	var disabled *Store
	disabled.Write("key", "ignored")
	if disabled.Read("key", time.Hour, &value) {
		t.Fatal("disabled cache was a hit")
	}
}
