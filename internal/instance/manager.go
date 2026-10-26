// Package instance manages game server processes: their configuration,
// lifecycle (start/stop/restart/crash recovery) and web console.
package instance

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gpanel/internal/events"
	"gpanel/internal/pty"
	"gpanel/internal/store"
)

var ErrNotFound = errors.New("实例不存在")

type Manager struct {
	baseDir string // default parent of instance work dirs
	store   *store.Collection[Config]
	bus     *events.Bus

	mu    sync.RWMutex
	items map[string]*Instance
	hooks []LaunchHook
}

func NewManager(dataDir string, bus *events.Bus) (*Manager, error) {
	st, err := store.Open[Config](filepath.Join(dataDir, "instances.json"))
	if err != nil {
		return nil, err
	}
	m := &Manager{baseDir: filepath.Join(dataDir, "instances"), store: st, bus: bus, items: map[string]*Instance{}}
	for _, cfg := range st.List() {
		if isLegacyDefaultDir(cfg) {
			cfg.WorkDir = ""
			if err := st.Put(cfg.ID, cfg); err != nil {
				return nil, err
			}
		}
		m.items[cfg.ID] = newInstance(m, cfg)
	}
	return m, nil
}

// isLegacyDefaultDir reports whether cfg stores the default work dir as an
// absolute path, as older versions did. Such paths break when the data dir
// moves; IDs are random, so "<anything>/instances/<id>" is never user-chosen.
func isLegacyDefaultDir(cfg Config) bool {
	return cfg.WorkDir != "" && filepath.Base(cfg.WorkDir) == cfg.ID &&
		filepath.Base(filepath.Dir(cfg.WorkDir)) == "instances"
}

// dir returns the effective working directory for cfg.
func (m *Manager) dir(cfg Config) string {
	if cfg.WorkDir == "" {
		return filepath.Join(m.baseDir, cfg.ID)
	}
	return cfg.WorkDir
}

// AddLaunchHook registers a hook run (in registration order) before every start.
func (m *Manager) AddLaunchHook(h LaunchHook) {
	m.mu.Lock()
	m.hooks = append(m.hooks, h)
	m.mu.Unlock()
}

func (m *Manager) launch(cfg Config, cols, rows uint16) (pty.Process, error) {
	dir := m.dir(cfg)
	spec := &LaunchSpec{Vars: map[string]string{"workdir": dir}, Env: map[string]string{}}
	m.mu.RLock()
	hooks := append([]LaunchHook(nil), m.hooks...)
	m.mu.RUnlock()
	for _, h := range hooks {
		if err := h(cfg, spec); err != nil {
			return nil, err
		}
	}
	argv, env, err := buildArgv(cfg, spec)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return pty.Start(pty.Spec{Path: argv[0], Args: argv[1:], Dir: dir, Env: env, Cols: cols, Rows: rows})
}

func (m *Manager) Get(id string) (*Instance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	inst, ok := m.items[id]
	if !ok {
		return nil, ErrNotFound
	}
	return inst, nil
}

func (m *Manager) List() []Status {
	m.mu.RLock()
	list := make([]*Instance, 0, len(m.items))
	for _, inst := range m.items {
		list = append(list, inst)
	}
	m.mu.RUnlock()
	out := make([]Status, 0, len(list))
	for _, inst := range list {
		out = append(out, inst.Status())
	}
	sortStatuses(out)
	return out
}

// WorkDir returns an instance's root directory (used by the file manager).
func (m *Manager) WorkDir(id string) (string, error) {
	inst, err := m.Get(id)
	if err != nil {
		return "", err
	}
	return m.dir(inst.Config()), nil
}

func (m *Manager) Create(cfg Config) (Config, error) {
	if err := cfg.normalize(); err != nil {
		return Config{}, err
	}
	cfg.ID = newID()
	cfg.CreatedAt = time.Now()
	if err := m.resolveWorkDir(&cfg); err != nil {
		return Config{}, err
	}
	if err := os.MkdirAll(m.dir(cfg), 0o755); err != nil {
		return Config{}, err
	}
	if err := m.store.Put(cfg.ID, cfg); err != nil {
		return Config{}, err
	}
	m.mu.Lock()
	m.items[cfg.ID] = newInstance(m, cfg)
	m.mu.Unlock()
	m.bus.Publish(events.Event{Type: events.InstanceCreated, InstanceID: cfg.ID, Data: cfg})
	return cfg, nil
}

// Update replaces an instance's config; changes apply on next start.
func (m *Manager) Update(id string, cfg Config) (Config, error) {
	inst, err := m.Get(id)
	if err != nil {
		return Config{}, err
	}
	old := inst.Config()
	cfg.ID, cfg.CreatedAt = old.ID, old.CreatedAt
	if cfg.Extensions == nil {
		cfg.Extensions = old.Extensions
	}
	if err := cfg.normalize(); err != nil {
		return Config{}, err
	}
	if err := m.resolveWorkDir(&cfg); err != nil {
		return Config{}, err
	}
	if err := m.store.Put(id, cfg); err != nil {
		return Config{}, err
	}
	inst.setConfig(cfg)
	m.bus.Publish(events.Event{Type: events.InstanceUpdated, InstanceID: id, Data: cfg})
	return cfg, nil
}

// Delete removes a stopped instance. Files are only removed when the work
// dir is the panel-managed default, never a user-chosen external path.
func (m *Manager) Delete(id string, removeFiles bool) error {
	inst, err := m.Get(id)
	if err != nil {
		return err
	}
	if inst.Running() {
		return errors.New("请先停止实例")
	}
	cfg := inst.Config()
	if err := m.store.Delete(id); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.items, id)
	m.mu.Unlock()
	if removeFiles && cfg.WorkDir == "" {
		if err := os.RemoveAll(m.dir(cfg)); err != nil {
			log.Printf("删除实例目录失败: %v", err)
		}
	}
	m.bus.Publish(events.Event{Type: events.InstanceDeleted, InstanceID: id})
	return nil
}

func (m *Manager) resolveWorkDir(cfg *Config) error {
	cfg.WorkDir = strings.TrimSpace(cfg.WorkDir)
	if cfg.WorkDir == "" {
		return nil
	}
	if !filepath.IsAbs(cfg.WorkDir) {
		return errors.New("工作目录必须是绝对路径")
	}
	cfg.WorkDir = filepath.Clean(cfg.WorkDir)
	if cfg.WorkDir == filepath.Join(m.baseDir, cfg.ID) {
		cfg.WorkDir = "" // the default; keep it relative to the data dir
	}
	return nil
}

// StartAutoStart launches every instance marked AutoStart.
func (m *Manager) StartAutoStart() {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, inst := range m.items {
		if inst.Config().AutoStart {
			go func(inst *Instance) {
				if err := inst.Start(); err != nil {
					log.Printf("自动启动实例 %s 失败: %v", inst.Config().Name, err)
				}
			}(inst)
		}
	}
}

// Shutdown gracefully stops all running instances, force-killing any that
// haven't exited by the deadline.
func (m *Manager) Shutdown(timeout time.Duration) {
	m.mu.RLock()
	var running []*Instance
	for _, inst := range m.items {
		if inst.Running() {
			running = append(running, inst)
		}
	}
	m.mu.RUnlock()

	deadline := time.After(timeout)
	for _, inst := range running {
		_ = inst.Stop()
	}
	for _, inst := range running {
		ch := inst.waitChan()
		if ch == nil {
			continue
		}
		select {
		case <-ch:
		case <-deadline:
			_ = inst.Kill()
			<-ch
		}
	}
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func sortStatuses(s []Status) {
	sort.Slice(s, func(a, b int) bool { return s[a].CreatedAt.Before(s[b].CreatedAt) })
}
