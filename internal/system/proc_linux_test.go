package system

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestParseStat(t *testing.T) {
	// Command names may contain spaces and ')'.
	line := "4242 (java (main) x) S 1 4200 4200 0 -1 4194560 100 0 0 0 150 25 0 0 20 0 30 0 123 456789 2048 18446744073709551615"
	pgrp, ticks, rss, ok := parseStat([]byte(line))
	if !ok || pgrp != 4200 || ticks != 175 || rss != 2048 {
		t.Fatalf("parseStat = %d %d %d %v", pgrp, ticks, rss, ok)
	}
	if _, _, _, ok := parseStat([]byte("1 (x) S 1")); ok {
		t.Fatal("short line accepted")
	}
}

func TestCPUTimesAndMemInfo(t *testing.T) {
	old := procRoot
	procRoot = t.TempDir()
	defer func() { procRoot = old }()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(procRoot, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// user nice system idle iowait irq softirq steal guest guest_nice
	write("stat", "cpu  100 10 50 800 40 0 0 0 999 999\ncpu0 1 1 1 1 1 1 1 1 1 1\n")
	write("meminfo", "MemTotal:       1000 kB\nMemFree:  10 kB\nMemAvailable:    600 kB\nSwapTotal: 200 kB\nSwapFree: 50 kB\n")

	total, idle, ok := cpuTimes()
	if !ok || total != 1000 || idle != 840 {
		t.Errorf("cpuTimes = %d %d %v, want 1000 840 (guest excluded)", total, idle, ok)
	}
	m, ok := memInfo()
	if !ok || m.total != 1000*1024 || m.available != 600*1024 || m.swapTotal != 200*1024 || m.swapFree != 50*1024 {
		t.Errorf("memInfo = %+v %v", m, ok)
	}
}

func TestMonitorReportsOwnProcessGroup(t *testing.T) {
	pgid := syscall.Getpgrp()
	m := NewMonitor(t.TempDir(), func() map[string]int { return map[string]int{"self": pgid} })
	m.sample()
	s := m.Snapshot()
	if !s.Supported || s.Host.CPUCores == 0 || s.Load.MemTotal == 0 || s.Load.DiskTotal == 0 {
		t.Fatalf("incomplete snapshot: %+v", s)
	}
	u, ok := s.Instances["self"]
	if !ok || u.Mem == 0 || u.Procs == 0 {
		t.Fatalf("own process group not measured: %+v", s.Instances)
	}
	if len(s.History) != 1 {
		t.Errorf("history has %d samples after one primed sample, want 1", len(s.History))
	}
}
