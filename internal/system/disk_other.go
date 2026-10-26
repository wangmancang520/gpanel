//go:build !linux && !darwin

package system

func diskUsage(path string) (total, free uint64, ok bool) { return 0, 0, false }
