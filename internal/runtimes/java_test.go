package runtimes

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// fakeJDK creates a minimal JDK layout that Java.Detect accepts.
func fakeJDK(t *testing.T, home string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "bin", javaBin()), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	release := "JAVA_VERSION=\"21.0.2\"\nIMPLEMENTOR=\"Eclipse Adoptium\"\n"
	if err := os.WriteFile(filepath.Join(home, "release"), []byte(release), 0o644); err != nil {
		t.Fatal(err)
	}
}

func managedRuntime(t *testing.T, dir string) Runtime {
	t.Helper()
	j := &Java{ManagedDir: dir}
	for _, rt := range j.Detect(context.Background()) {
		if rt.Managed {
			return rt
		}
	}
	t.Fatal("managed runtime not detected")
	return Runtime{}
}

func TestManagedRuntimeIDSurvivesMove(t *testing.T) {
	a := filepath.Join(t.TempDir(), "runtimes", "java")
	fakeJDK(t, filepath.Join(a, "jdk-21.0.2+13-jre"))
	before := managedRuntime(t, a)
	if before.ManagedPath != "jdk-21.0.2+13-jre" {
		t.Errorf("ManagedPath = %q", before.ManagedPath)
	}

	b := filepath.Join(t.TempDir(), "elsewhere", "java")
	if err := os.MkdirAll(filepath.Dir(b), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(a, b); err != nil {
		t.Fatal(err)
	}
	after := managedRuntime(t, b)
	if after.ID != before.ID {
		t.Fatalf("ID changed after moving the data dir: %s -> %s", before.ID, after.ID)
	}
}

func TestRegistryGetAcceptsLegacyID(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "java")
	fakeJDK(t, filepath.Join(dir, "jdk21"))
	r := NewRegistry(&Java{ManagedDir: dir})
	r.Scan(context.Background())
	rt := managedRuntime(t, dir)

	home, _ := filepath.EvalSymlinks(filepath.Join(dir, "jdk21"))
	legacy := runtimeID("java", home)
	if legacy == rt.ID {
		t.Fatal("legacy and new IDs should differ")
	}
	got, ok := r.Get(legacy)
	if !ok || got.ID != rt.ID {
		t.Fatalf("Get(legacy) = %+v, %v; want runtime %s", got, ok, rt.ID)
	}
}
