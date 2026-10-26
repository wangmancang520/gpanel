//go:build !linux

package system

// TODO: macOS (host_statistics / sysctl) and Windows (GetSystemTimes,
// GlobalMemoryStatusEx). Until then the dashboard shows host basics only.
const supported = false

func hostInfo(h *Host) {}

func cpuTimes() (total, idle uint64, ok bool) { return 0, 0, false }

func memInfo() (memStats, bool) { return memStats{}, false }

func loadAvg() (l1, l5, l15 float64, ok bool) { return 0, 0, 0, false }

func groupUsage(pgids []int) map[int]groupStat { return map[int]groupStat{} }
