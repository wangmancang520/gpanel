//go:build windows

package pty

import "errors"

// TODO: implement with ConPTY (e.g. github.com/aymanbagabas/go-pty) and
// job objects for Terminate/Kill of the whole process tree.
func Start(spec Spec) (Process, error) {
	return nil, errors.New("Windows 终端尚未实现")
}
