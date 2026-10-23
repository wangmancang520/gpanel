// Package pty starts a child process attached to a pseudo-terminal.
//
// The implementation is per-platform (pty_unix.go, pty_windows.go) behind
// the Process interface, so the rest of the panel never touches OS details.
package pty

import "io"

type Spec struct {
	Path string   // executable; bare names are looked up in Env's PATH
	Args []string // arguments, not including the executable
	Dir  string
	Env  []string // full environment, "KEY=VALUE"
	Cols uint16
	Rows uint16
}

type Process interface {
	io.Reader // terminal output
	io.Writer // terminal input
	Resize(cols, rows uint16) error
	Pid() int
	// Terminate asks the whole process tree to exit (SIGTERM on Unix).
	Terminate() error
	// Kill force-kills the whole process tree.
	Kill() error
	// Wait blocks until the process exits and returns its exit code.
	Wait() (int, error)
	Close() error
}
