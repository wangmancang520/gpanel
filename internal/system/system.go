// Package system reports host configuration and load for the dashboard:
// CPU, memory, disk, plus per-instance CPU and memory of running processes.
//
// A background sampler keeps a short history so the page can draw trends
// without polling the OS on every request. The OS-specific readers live in
// proc_linux.go; other platforms report host basics only (Supported=false).
package system

import (
	"context"
	"math"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"

	"gpanel/internal/httpx"
)

const (
	sampleInterval = 2 * time.Second
	historyLen     = 90 // 3 minutes at sampleInterval
)

type Host struct {
	Hostname  string    `json:"hostname"`
	OS        string    `json:"os"` // distribution name when known, else GOOS
	Kernel    string    `json:"kernel,omitempty"`
	Arch      string    `json:"arch"`
	CPUModel  string    `json:"cpuModel,omitempty"`
	CPUCores  int       `json:"cpuCores"` // logical CPUs
	MemTotal  uint64    `json:"memTotal"`
	SwapTotal uint64    `json:"swapTotal"`
	BootTime  time.Time `json:"bootTime,omitzero"`
	GoVersion string    `json:"goVersion"`
	StartedAt time.Time `json:"startedAt"` // panel process start
	DataDir   string    `json:"dataDir"`
}

type Load struct {
	CPU       float64   `json:"cpu"`   // percent of all CPUs, 0-100
	Cores     []float64 `json:"cores"` // per logical CPU, 0-100
	Load1     float64   `json:"load1"`
	Load5     float64   `json:"load5"`
	Load15    float64   `json:"load15"`
	MemUsed   uint64    `json:"memUsed"` // total minus available
	MemTotal  uint64    `json:"memTotal"`
	SwapUsed  uint64    `json:"swapUsed"`
	SwapTotal uint64    `json:"swapTotal"`
	DiskUsed  uint64    `json:"diskUsed"` // filesystem holding the data dir
	DiskTotal uint64    `json:"diskTotal"`
}

type Sample struct {
	Time    time.Time `json:"t"`
	CPU     float64   `json:"cpu"`
	Cores   []float64 `json:"cores,omitempty"`
	MemUsed uint64    `json:"mem"`
}

// Usage is the resource use of one instance's whole process group.
type Usage struct {
	CPU   float64 `json:"cpu"` // percent of all CPUs, 0-100
	Mem   uint64  `json:"mem"` // resident set size, bytes
	Procs int     `json:"procs"`
}

type Snapshot struct {
	Supported bool             `json:"supported"` // false: no load data on this OS
	Host      Host             `json:"host"`
	Load      Load             `json:"load"`
	History   []Sample         `json:"history"`
	Instances map[string]Usage `json:"instances"` // by instance ID
}

// Monitor samples the host periodically. Pids maps instance IDs to the pid
// of their process-group leader (instances run in their own session).
type Monitor struct {
	dataDir string
	pids    func() map[string]int

	mu        sync.RWMutex
	snap      Snapshot
	prev      cpuTick        // all CPUs at the last sample
	prevCores []cpuTick      // per logical CPU at the last sample
	prevGroup map[int]uint64 // pgid -> CPU ticks at the last sample
}

func NewMonitor(dataDir string, pids func() map[string]int) *Monitor {
	h := Host{
		OS: runtime.GOOS, Arch: runtime.GOARCH, CPUCores: runtime.NumCPU(),
		GoVersion: runtime.Version(), StartedAt: time.Now(), DataDir: dataDir,
	}
	h.Hostname, _ = os.Hostname()
	hostInfo(&h)
	m := &Monitor{dataDir: dataDir, pids: pids, prevGroup: map[int]uint64{}}
	m.snap = Snapshot{Supported: supported, Host: h, History: []Sample{}, Instances: map[string]Usage{}}
	m.sample() // primes CPU counters; the first percentages come from the next sample
	return m
}

// Run samples until ctx is cancelled.
func (m *Monitor) Run(ctx context.Context) {
	t := time.NewTicker(sampleInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.sample()
		}
	}
}

func (m *Monitor) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.snap
	s.History = append([]Sample(nil), s.History...)
	s.Instances = make(map[string]Usage, len(m.snap.Instances))
	for k, v := range m.snap.Instances {
		s.Instances[k] = v
	}
	return s
}

func (m *Monitor) sample() {
	var load Load
	if mem, ok := memInfo(); ok {
		load.MemTotal, load.MemUsed = mem.total, mem.total-min(mem.available, mem.total)
		load.SwapTotal, load.SwapUsed = mem.swapTotal, mem.swapTotal-min(mem.swapFree, mem.swapTotal)
	}
	load.Load1, load.Load5, load.Load15, _ = loadAvg()
	if total, free, ok := diskUsage(m.dataDir); ok {
		load.DiskTotal, load.DiskUsed = total, total-min(free, total)
	}

	pids := m.pids()
	pgids := make([]int, 0, len(pids))
	for _, pid := range pids {
		pgids = append(pgids, pid)
	}
	groups := groupUsage(pgids)
	all, cores, cpuOK := cpuTimes()

	m.mu.Lock()
	defer m.mu.Unlock()
	dTotal := all.total - m.prev.total
	primed := m.prev.total != 0 && cpuOK && all.total > m.prev.total
	if primed {
		load.CPU = busy(all, m.prev)
		// CPUs going on/offline change the count; skip one round then.
		if len(cores) == len(m.prevCores) {
			load.Cores = make([]float64, len(cores))
			for i, c := range cores {
				load.Cores[i] = round1(busy(c, m.prevCores[i]))
			}
		}
	}
	instances := map[string]Usage{}
	prevGroup := map[int]uint64{}
	for id, pid := range pids {
		g, ok := groups[pid]
		if !ok {
			continue
		}
		u := Usage{Mem: g.rss, Procs: g.procs}
		// A restarted instance reuses the map slot under a new pgid, so a
		// missing or larger previous value just means no delta yet.
		if prev, ok := m.prevGroup[pid]; ok && primed && g.ticks >= prev {
			u.CPU = percent(g.ticks-prev, dTotal)
		}
		instances[id] = u
		prevGroup[pid] = g.ticks
	}
	if cpuOK {
		m.prev, m.prevCores = all, cores
	}
	m.prevGroup = prevGroup

	m.snap.Load = load
	m.snap.Instances = instances
	if load.MemTotal > 0 {
		m.snap.Host.MemTotal, m.snap.Host.SwapTotal = load.MemTotal, load.SwapTotal
	}
	if primed {
		m.snap.History = append(m.snap.History, Sample{Time: time.Now(), CPU: load.CPU, Cores: load.Cores, MemUsed: load.MemUsed})
		if n := len(m.snap.History); n > historyLen {
			m.snap.History = append(m.snap.History[:0:0], m.snap.History[n-historyLen:]...)
		}
	}
}

// busy is the non-idle share of the ticks elapsed between prev and cur.
func busy(cur, prev cpuTick) float64 {
	if cur.total <= prev.total {
		return 0
	}
	d := cur.total - prev.total
	return percent(d-min(cur.idle-prev.idle, d), d)
}

// round1 keeps one decimal: per-core history is sent every poll.
func round1(v float64) float64 { return math.Round(v*10) / 10 }

func percent(part, whole uint64) float64 {
	if whole == 0 {
		return 0
	}
	return float64(part) * 100 / float64(whole)
}

func (m *Monitor) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/system", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, m.Snapshot())
	})
}

type memStats struct{ total, available, swapTotal, swapFree uint64 }

type cpuTick struct{ total, idle uint64 } // jiffies

type groupStat struct {
	ticks uint64 // user + system CPU time, in the same unit as cpuTimes
	rss   uint64 // bytes
	procs int
}
