package system

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const supported = true

// procRoot is a variable so tests can point the readers at a fake /proc.
var procRoot = "/proc"

func procPath(elem ...string) string { return filepath.Join(append([]string{procRoot}, elem...)...) }

func hostInfo(h *Host) {
	if b, err := os.ReadFile(procPath("sys", "kernel", "osrelease")); err == nil {
		h.Kernel = strings.TrimSpace(string(b))
	}
	if name := osRelease("/etc/os-release"); name != "" {
		h.OS = name
	}
	if f, err := os.Open(procPath("cpuinfo")); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			k, v, ok := strings.Cut(sc.Text(), ":")
			// x86 uses "model name"; many ARM kernels only have "Model" / "Hardware".
			if ok && slices.Contains([]string{"model name", "Model", "Hardware"}, strings.TrimSpace(k)) {
				h.CPUModel = strings.TrimSpace(v)
				break
			}
		}
		f.Close()
	}
	if b, err := os.ReadFile(procPath("stat")); err == nil {
		for line := range strings.Lines(string(b)) {
			if v, ok := strings.CutPrefix(line, "btime "); ok {
				if sec, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
					h.BootTime = time.Unix(sec, 0)
				}
			}
		}
	}
}

func osRelease(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for line := range strings.Lines(string(b)) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "PRETTY_NAME="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

// cpuTimes returns jiffies from /proc/stat: the aggregate over all CPUs and
// one entry per logical CPU, in kernel order.
func cpuTimes() (all cpuTick, cores []cpuTick, ok bool) {
	b, err := os.ReadFile(procPath("stat"))
	if err != nil {
		return all, nil, false
	}
	for line := range strings.Lines(string(b)) {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.HasPrefix(fields[0], "cpu") {
			continue
		}
		t := parseCPU(fields[1:])
		if fields[0] == "cpu" {
			all, ok = t, true
		} else {
			cores = append(cores, t)
		}
	}
	return all, cores, ok
}

// parseCPU sums "user nice system idle iowait irq softirq steal [guest
// guest_nice]"; guest time is already counted in user/nice, so it stops there.
func parseCPU(fields []string) cpuTick {
	var t cpuTick
	for i, f := range fields[:min(len(fields), 8)] {
		v, _ := strconv.ParseUint(f, 10, 64)
		t.total += v
		if i == 3 || i == 4 { // idle, iowait
			t.idle += v
		}
	}
	return t
}

func memInfo() (memStats, bool) {
	f, err := os.Open(procPath("meminfo"))
	if err != nil {
		return memStats{}, false
	}
	defer f.Close()
	var m memStats
	fields := map[string]*uint64{
		"MemTotal": &m.total, "MemAvailable": &m.available,
		"SwapTotal": &m.swapTotal, "SwapFree": &m.swapFree,
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if dst, want := fields[k]; ok && want {
			n, _ := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			*dst = n * 1024
		}
	}
	return m, m.total > 0
}

func loadAvg() (l1, l5, l15 float64, ok bool) {
	b, err := os.ReadFile(procPath("loadavg"))
	if err != nil {
		return 0, 0, 0, false
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return 0, 0, 0, false
	}
	l1, _ = strconv.ParseFloat(f[0], 64)
	l5, _ = strconv.ParseFloat(f[1], 64)
	l15, _ = strconv.ParseFloat(f[2], 64)
	return l1, l5, l15, true
}

// groupUsage sums CPU time and RSS of every process in the given process
// groups, so a wrapper script and the JVM it launches count together.
func groupUsage(pgids []int) map[int]groupStat {
	out := map[int]groupStat{}
	if len(pgids) == 0 {
		return out
	}
	want := map[int]bool{}
	for _, p := range pgids {
		want[p] = true
	}
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return out
	}
	page := uint64(os.Getpagesize())
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(procPath(e.Name(), "stat"))
		if err != nil {
			continue // exited meanwhile
		}
		pgrp, ticks, rss, ok := parseStat(b)
		if !ok || !want[pgrp] {
			continue
		}
		g := out[pgrp]
		g.ticks += ticks
		g.rss += rss * page
		g.procs++
		out[pgrp] = g
	}
	return out
}

// parseStat extracts pgrp, utime+stime and rss (pages) from /proc/<pid>/stat.
// The command name may contain spaces and parentheses, so fields are
// counted from the last ')'.
func parseStat(b []byte) (pgrp int, ticks, rss uint64, ok bool) {
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return 0, 0, 0, false
	}
	f := strings.Fields(string(b[i+1:]))
	// f[0] is field 3 (state); field N is f[N-3].
	if len(f) < 22 {
		return 0, 0, 0, false
	}
	pgrp, _ = strconv.Atoi(f[2])
	utime, _ := strconv.ParseUint(f[11], 10, 64)
	stime, _ := strconv.ParseUint(f[12], 10, 64)
	rss, _ = strconv.ParseUint(f[21], 10, 64)
	return pgrp, utime + stime, rss, true
}
