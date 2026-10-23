//go:build !windows

package pty

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	cpty "github.com/creack/pty"
)

type unixProcess struct {
	cmd *exec.Cmd
	f   *os.File
}

func Start(spec Spec) (Process, error) {
	path, err := lookPath(spec.Path, spec.Env)
	if err != nil {
		return nil, err
	}
	cmd := &exec.Cmd{
		Path: path,
		Args: append([]string{spec.Path}, spec.Args...),
		Dir:  spec.Dir,
		Env:  spec.Env,
	}
	// creack/pty puts the child in a new session (Setsid), so its pid is
	// also the process-group id and we can signal the whole tree.
	f, err := cpty.StartWithSize(cmd, &cpty.Winsize{Cols: spec.Cols, Rows: spec.Rows})
	if err != nil {
		return nil, err
	}
	return &unixProcess{cmd: cmd, f: f}, nil
}

func (p *unixProcess) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p *unixProcess) Write(b []byte) (int, error) { return p.f.Write(b) }
func (p *unixProcess) Pid() int                    { return p.cmd.Process.Pid }
func (p *unixProcess) Close() error                { return p.f.Close() }

func (p *unixProcess) Resize(cols, rows uint16) error {
	return cpty.Setsize(p.f, &cpty.Winsize{Cols: cols, Rows: rows})
}

func (p *unixProcess) Terminate() error { return syscall.Kill(-p.Pid(), syscall.SIGTERM) }
func (p *unixProcess) Kill() error      { return syscall.Kill(-p.Pid(), syscall.SIGKILL) }

func (p *unixProcess) Wait() (int, error) {
	err := p.cmd.Wait()
	var exitErr *exec.ExitError
	if err == nil || errors.As(err, &exitErr) {
		return p.cmd.ProcessState.ExitCode(), nil
	}
	return -1, err
}

// lookPath resolves bare command names against the child's PATH (which a
// runtime such as Java may have prepended to), not the panel's own PATH.
// Relative paths like ./start.sh are left for exec to resolve against Dir.
func lookPath(name string, env []string) (string, error) {
	if strings.ContainsRune(name, '/') {
		return name, nil
	}
	pathEnv := os.Getenv("PATH")
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			pathEnv = v
		}
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("找不到可执行文件 %q", name)
}
