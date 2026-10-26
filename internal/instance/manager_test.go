package instance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gpanel/internal/events"
)

func TestDefaultWorkDirFollowsDataDir(t *testing.T) {
	data := t.TempDir()
	m, err := NewManager(data, events.NewBus())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := m.Create(Config{Name: "a", Command: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkDir != "" {
		t.Fatalf("default work dir persisted as %q, want empty", cfg.WorkDir)
	}

	// Move the whole data dir; the instance must follow it.
	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(data, moved); err != nil {
		t.Fatal(err)
	}
	m, err = NewManager(moved, events.NewBus())
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(moved, "instances", cfg.ID)
	if got, _ := m.WorkDir(cfg.ID); got != want {
		t.Fatalf("WorkDir = %q, want %q", got, want)
	}
	if st := m.List()[0]; st.Dir != want {
		t.Fatalf("Status.Dir = %q, want %q", st.Dir, want)
	}
}

func TestLegacyAbsoluteWorkDirMigrated(t *testing.T) {
	data := t.TempDir()
	cfgs := map[string]Config{
		"aaa": {ID: "aaa", Name: "legacy", Command: "true", WorkDir: "/old/place/data/instances/aaa"},
		"bbb": {ID: "bbb", Name: "custom", Command: "true", WorkDir: "/srv/mc"},
	}
	raw, _ := json.Marshal(cfgs)
	if err := os.WriteFile(filepath.Join(data, "instances.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(data, events.NewBus())
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := m.WorkDir("aaa"); got != filepath.Join(data, "instances", "aaa") {
		t.Errorf("legacy default dir resolved to %q", got)
	}
	if got, _ := m.WorkDir("bbb"); got != "/srv/mc" {
		t.Errorf("custom dir changed to %q", got)
	}
	saved, _ := os.ReadFile(filepath.Join(data, "instances.json"))
	var back map[string]Config
	if err := json.Unmarshal(saved, &back); err != nil {
		t.Fatal(err)
	}
	if back["aaa"].WorkDir != "" || back["bbb"].WorkDir != "/srv/mc" {
		t.Errorf("persisted work dirs = %q, %q", back["aaa"].WorkDir, back["bbb"].WorkDir)
	}
}

func TestExplicitDefaultWorkDirStoredEmpty(t *testing.T) {
	data := t.TempDir()
	m, err := NewManager(data, events.NewBus())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := m.Create(Config{Name: "a", Command: "true"})
	if err != nil {
		t.Fatal(err)
	}
	cfg.WorkDir = filepath.Join(data, "instances", cfg.ID) + "/"
	if cfg, err = m.Update(cfg.ID, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.WorkDir != "" {
		t.Fatalf("explicit default dir stored as %q, want empty", cfg.WorkDir)
	}
}
