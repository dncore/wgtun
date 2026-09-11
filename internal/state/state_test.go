package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPersistenceRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("wg0", Instance{Enabled: true, DesiredRun: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update("wg1", func(in *Instance) { in.DesiredRun = true }); err != nil {
		t.Fatal(err)
	}

	s2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Get("wg0"); !got.Enabled || !got.DesiredRun {
		t.Fatalf("wg0 state lost: %+v", got)
	}
	if got := s2.Get("wg1"); got.Enabled || !got.DesiredRun {
		t.Fatalf("wg1 state wrong: %+v", got)
	}
	if got := s2.Get("missing"); got != (Instance{}) {
		t.Fatalf("unknown instance must be zero value, got %+v", got)
	}
	if names := s2.Names(); len(names) != 2 || names[0] != "wg0" || names[1] != "wg1" {
		t.Fatalf("names: %v", names)
	}
}

func TestRemoveAndMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Set("wg0", Instance{Enabled: true})
	s.Remove("wg0")
	if names := s.Names(); len(names) != 0 {
		t.Fatalf("remove failed: %v", names)
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("state file should exist after save")
	}
}

func TestCorruptFileFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	os.WriteFile(path, []byte("{not json"), 0o640)
	if _, err := Load(path); err == nil {
		t.Fatal("want error for corrupt state file")
	}
}
