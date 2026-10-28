//go:build !linux && !darwin

package files

func diskFree(path string) (uint64, bool) { return 0, false }
