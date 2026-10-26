package instance

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"gpanel/internal/events"
	"gpanel/internal/pty"
)

const (
	historySize      = 512 * 1024 // bytes of console output kept for replay
	subscriberBuffer = 256        // output chunks queued per terminal before it is dropped
	maxCrashRestarts = 3
	restartDelay     = 5 * time.Second
	killGrace        = 10 * time.Second
)

var (
	ErrNotRunning     = errors.New("实例未运行")
	ErrAlreadyRunning = errors.New("实例已在运行")
)

type subscriber struct{ ch chan []byte }

// Instance owns one game server process and its console.
type Instance struct {
	m *Manager

	mu             sync.Mutex
	cfg            Config
	state          State
	proc           pty.Process
	done           chan struct{} // closed when the current process exits
	startedAt      time.Time
	exitCode       *int
	lastErr        string
	stopRequested  bool
	restartPending bool
	restartTimer   *time.Timer
	crashCount     int
	cols, rows     uint16
	history        *ringBuffer
	subs           map[*subscriber]struct{}
}

func newInstance(m *Manager, cfg Config) *Instance {
	return &Instance{
		m: m, cfg: cfg, state: StateStopped,
		cols: 120, rows: 30,
		history: newRingBuffer(historySize),
		subs:    map[*subscriber]struct{}{},
	}
}

func (i *Instance) Config() Config {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.cfg
}

func (i *Instance) setConfig(cfg Config) {
	i.mu.Lock()
	i.cfg = cfg
	i.mu.Unlock()
}

func (i *Instance) Status() Status {
	i.mu.Lock()
	defer i.mu.Unlock()
	s := Status{Config: i.cfg, Dir: i.m.dir(i.cfg), State: i.state, ExitCode: i.exitCode, LastError: i.lastErr}
	if i.proc != nil {
		s.Pid = i.proc.Pid()
		t := i.startedAt
		s.StartedAt = &t
	}
	return s
}

func (i *Instance) Running() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.proc != nil || i.state == StateStarting
}

func (i *Instance) publish() {
	s := i.Status()
	i.m.bus.Publish(events.Event{Type: events.InstanceState, InstanceID: s.ID, Data: s})
}

// emitLocked appends output to history and fans it out to attached terminals.
func (i *Instance) emitLocked(b []byte) {
	i.history.Write(b)
	for sub := range i.subs {
		select {
		case sub.ch <- b:
		default: // client can't keep up; drop it rather than stall the game server
			delete(i.subs, sub)
			close(sub.ch)
		}
	}
}

func (i *Instance) notice(format string, args ...any) {
	i.mu.Lock()
	i.noticeLocked(format, args...)
	i.mu.Unlock()
}

func (i *Instance) noticeLocked(format string, args ...any) {
	i.emitLocked([]byte("\r\n\x1b[36m[gpanel] " + fmt.Sprintf(format, args...) + "\x1b[0m\r\n"))
}

// Attach returns buffered history and a live output stream. The channel is
// closed if the client falls too far behind; call detach when done.
func (i *Instance) Attach() (history []byte, out <-chan []byte, detach func()) {
	sub := &subscriber{ch: make(chan []byte, subscriberBuffer)}
	i.mu.Lock()
	history = i.history.Bytes()
	i.subs[sub] = struct{}{}
	i.mu.Unlock()
	return history, sub.ch, func() {
		i.mu.Lock()
		if _, ok := i.subs[sub]; ok {
			delete(i.subs, sub)
			close(sub.ch)
		}
		i.mu.Unlock()
	}
}

func (i *Instance) Start() error {
	i.mu.Lock()
	if i.proc != nil || i.state == StateStarting {
		i.mu.Unlock()
		return ErrAlreadyRunning
	}
	i.cancelRestartLocked()
	cfg, cols, rows := i.cfg, i.cols, i.rows
	i.state, i.lastErr, i.stopRequested = StateStarting, "", false
	i.mu.Unlock()
	i.publish()

	proc, err := i.m.launch(cfg, cols, rows)

	i.mu.Lock()
	if err != nil {
		i.state, i.lastErr = StateStopped, err.Error()
		i.noticeLocked("启动失败：%v", err)
		i.mu.Unlock()
		i.publish()
		return err
	}
	done := make(chan struct{})
	i.proc, i.done, i.startedAt, i.exitCode, i.state = proc, done, time.Now(), nil, StateRunning
	i.noticeLocked("实例已启动 (PID %d)", proc.Pid())
	i.mu.Unlock()
	i.publish()

	go i.supervise(proc, done)
	return nil
}

// supervise pumps output until the process exits, then decides what happens next.
func (i *Instance) supervise(proc pty.Process, done chan struct{}) {
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 32*1024)
		for {
			n, err := proc.Read(buf)
			if n > 0 {
				chunk := append([]byte(nil), buf[:n]...)
				i.mu.Lock()
				i.emitLocked(chunk)
				i.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	code, werr := proc.Wait()
	// Let the reader drain remaining output; orphaned grandchildren could
	// keep the pty open forever, so don't wait indefinitely.
	select {
	case <-readDone:
	case <-time.After(time.Second):
	}
	proc.Close()

	i.mu.Lock()
	uptime := time.Since(i.startedAt)
	i.proc, i.exitCode = nil, &code
	close(done)
	if werr != nil {
		i.lastErr = werr.Error()
	}
	if i.stopRequested || code == 0 {
		i.state = StateStopped
	} else {
		i.state = StateCrashed
	}
	i.noticeLocked("进程已退出，退出码 %d", code)

	restart := i.restartPending
	i.restartPending = false
	delay := time.Duration(0)
	if !restart && i.state == StateCrashed && i.cfg.AutoRestart {
		if uptime > time.Minute {
			i.crashCount = 0
		}
		i.crashCount++
		if i.crashCount <= maxCrashRestarts {
			restart, delay = true, restartDelay
			i.noticeLocked("将在 %d 秒后自动重启 (%d/%d)", int(delay.Seconds()), i.crashCount, maxCrashRestarts)
		} else {
			i.noticeLocked("连续崩溃次数过多，已停止自动重启")
		}
	}
	if restart {
		id := i.cfg.ID
		i.restartTimer = time.AfterFunc(delay, func() {
			if err := i.Start(); err != nil {
				log.Printf("实例 %s 重启失败: %v", id, err)
			}
		})
	}
	i.mu.Unlock()
	i.publish()
}

func (i *Instance) cancelRestartLocked() bool {
	if i.restartTimer == nil {
		return false
	}
	stopped := i.restartTimer.Stop()
	i.restartTimer = nil
	return stopped
}

// Stop sends the configured stop command (or SIGTERM), escalating to
// SIGTERM and then SIGKILL if the process doesn't exit in time.
func (i *Instance) Stop() error {
	i.mu.Lock()
	cancelled := i.cancelRestartLocked()
	if i.proc == nil {
		i.mu.Unlock()
		if cancelled {
			return nil
		}
		return ErrNotRunning
	}
	if i.state == StateStopping {
		i.mu.Unlock()
		return errors.New("正在停止中，如无响应请使用强制结束")
	}
	proc, done := i.proc, i.done
	stopCmd, timeout := i.cfg.StopCommand, time.Duration(i.cfg.StopTimeout)*time.Second
	i.state, i.stopRequested = StateStopping, true
	i.mu.Unlock()
	i.publish()

	if stopCmd != "" {
		if _, err := proc.Write([]byte(stopCmd + "\r")); err != nil {
			_ = proc.Terminate()
		}
	} else {
		_ = proc.Terminate()
	}

	go func() {
		select {
		case <-done:
			return
		case <-time.After(timeout):
		}
		i.notice("停止超时，发送终止信号")
		_ = proc.Terminate()
		select {
		case <-done:
			return
		case <-time.After(killGrace):
		}
		i.notice("进程仍未退出，强制结束")
		_ = proc.Kill()
	}()
	return nil
}

func (i *Instance) Kill() error {
	i.mu.Lock()
	i.cancelRestartLocked()
	proc := i.proc
	if proc == nil {
		i.mu.Unlock()
		return ErrNotRunning
	}
	i.stopRequested, i.restartPending = true, false
	i.mu.Unlock()
	return proc.Kill()
}

func (i *Instance) Restart() error {
	i.mu.Lock()
	if i.proc == nil {
		i.mu.Unlock()
		return i.Start()
	}
	i.restartPending = true
	i.mu.Unlock()
	return i.Stop()
}

// Input writes raw bytes (keystrokes) to the console.
func (i *Instance) Input(b []byte) error {
	i.mu.Lock()
	proc := i.proc
	i.mu.Unlock()
	if proc == nil {
		return ErrNotRunning
	}
	_, err := proc.Write(b)
	return err
}

// SendCommand types a line into the console followed by Enter.
func (i *Instance) SendCommand(line string) error {
	return i.Input([]byte(line + "\r"))
}

func (i *Instance) Resize(cols, rows uint16) {
	if cols == 0 || rows == 0 {
		return
	}
	i.mu.Lock()
	i.cols, i.rows = cols, rows
	proc := i.proc
	i.mu.Unlock()
	if proc != nil {
		_ = proc.Resize(cols, rows)
	}
}

// waitChan returns a channel closed when the current process exits, or nil.
func (i *Instance) waitChan() <-chan struct{} {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.proc == nil {
		return nil
	}
	return i.done
}
