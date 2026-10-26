// Package runtimes discovers language runtimes on this machine (Java today;
// Node, .NET, ... later) and lets an instance pick one to launch with.
//
// Each kind of runtime is a Provider. The Registry merges their results and
// plugs into instance launches through a LaunchHook: the selected runtime
// supplies command variables like {{java}} plus environment (JAVA_HOME, PATH).
package runtimes

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log"
	"sort"
	"sync"

	"gpanel/internal/instance"
)

type Runtime struct {
	// ID is stable: kind + hash of Home, or for managed runtimes of the path
	// inside the managed dir, so it survives moving the data dir.
	ID         string `json:"id"`
	Kind       string `json:"kind"` // "java", ...
	Name       string `json:"name"` // display name
	Version    string `json:"version"`
	Major      int    `json:"major"`
	Vendor     string `json:"vendor,omitempty"`
	Home       string `json:"home"`
	Executable string `json:"executable"`
	Managed    bool   `json:"managed"` // lives in the panel's own runtimes dir
	// ManagedPath is Home relative to the managed dir (managed runtimes only).
	ManagedPath string `json:"managedPath,omitempty"`

	// legacyID is the ID older versions derived from the absolute Home; Get
	// still accepts it so existing instance configs can be migrated.
	legacyID string
}

// Provider detects runtimes of one kind and describes how to launch with them.
type Provider interface {
	Kind() string
	Detect(ctx context.Context) []Runtime
	// Launch returns command variables and env for a selected runtime.
	Launch(rt Runtime) (vars, env map[string]string, pathPrepend []string)
	// Defaults returns variables used when an instance selects no runtime of
	// this kind, so "{{java}}" still works with the system Java.
	Defaults() map[string]string
}

// Installer is an optional Provider extension for downloadable runtimes.
// The provider only lists releases; downloading, checksum verification and
// extraction into InstallDir are shared (see install.go).
type Installer interface {
	Releases(ctx context.Context) ([]Release, error)
	// InstallDir is the managed directory; each release unpacks into InstallDir/<Release.ID>.
	InstallDir() string
}

type Release struct {
	ID        string `json:"id"` // also the install directory name
	Kind      string `json:"kind"`
	Vendor    string `json:"vendor"`
	Version   string `json:"version"`
	Major     int    `json:"major"`
	ImageType string `json:"imageType"` // "jdk" / "jre"
	LTS       bool   `json:"lts"`
	FileName  string `json:"fileName"`
	URL       string `json:"url"`
	MirrorURL string `json:"mirrorUrl,omitempty"` // tried first, falls back to URL
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	Installed bool   `json:"installed"`
}

type Registry struct {
	providers []Provider

	// InUse reports which instances use a runtime; set by main so that
	// uninstalling can refuse runtimes still referenced.
	InUse func(runtimeID string) []string

	mu    sync.RWMutex
	items map[string]Runtime

	jobsMu sync.Mutex
	jobs   map[string]*job
}

func NewRegistry(providers ...Provider) *Registry {
	return &Registry{providers: providers, items: map[string]Runtime{}, jobs: map[string]*job{}}
}

// Scan re-runs detection for every provider.
func (r *Registry) Scan(ctx context.Context) {
	items := map[string]Runtime{}
	for _, p := range r.providers {
		for _, rt := range p.Detect(ctx) {
			items[rt.ID] = rt
		}
	}
	r.mu.Lock()
	r.items = items
	r.mu.Unlock()
	log.Printf("检测到 %d 个运行时", len(items))
}

// List returns runtimes, optionally filtered by kind, newest version first.
func (r *Registry) List(kind string) []Runtime {
	r.mu.RLock()
	out := make([]Runtime, 0, len(r.items))
	for _, rt := range r.items {
		if kind == "" || rt.Kind == kind {
			out = append(out, rt)
		}
	}
	r.mu.RUnlock()
	sort.Slice(out, func(a, b int) bool {
		if out[a].Kind != out[b].Kind {
			return out[a].Kind < out[b].Kind
		}
		if out[a].Major != out[b].Major {
			return out[a].Major > out[b].Major
		}
		return out[a].Home < out[b].Home
	})
	return out
}

// Get looks a runtime up by ID, also accepting legacy IDs; compare the
// returned Runtime.ID with id to detect a reference that needs migrating.
func (r *Registry) Get(id string) (Runtime, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if rt, ok := r.items[id]; ok {
		return rt, true
	}
	for _, rt := range r.items {
		if rt.legacyID != "" && rt.legacyID == id {
			return rt, true
		}
	}
	return Runtime{}, false
}

func (r *Registry) provider(kind string) Provider {
	for _, p := range r.providers {
		if p.Kind() == kind {
			return p
		}
	}
	return nil
}

// LaunchHook applies provider defaults, then the instance's selected runtime.
func (r *Registry) LaunchHook() instance.LaunchHook {
	return func(cfg instance.Config, spec *instance.LaunchSpec) error {
		for _, p := range r.providers {
			for k, v := range p.Defaults() {
				spec.Vars[k] = v
			}
		}
		if cfg.Runtime == "" {
			return nil
		}
		rt, ok := r.Get(cfg.Runtime)
		if !ok {
			return fmt.Errorf("所选运行时 %s 不存在，请重新扫描或在实例设置中更换", cfg.Runtime)
		}
		p := r.provider(rt.Kind)
		if p == nil {
			return fmt.Errorf("不支持的运行时类型 %s", rt.Kind)
		}
		vars, env, prepend := p.Launch(rt)
		for k, v := range vars {
			spec.Vars[k] = v
		}
		for k, v := range env {
			spec.Env[k] = v
		}
		spec.PathPrepend = append(spec.PathPrepend, prepend...)
		return nil
	}
}

// runtimeID hashes key: the absolute home for system runtimes, or
// "managed:" + the slash-separated path inside the managed dir.
func runtimeID(kind, key string) string {
	sum := sha1.Sum([]byte(key))
	return kind + "-" + hex.EncodeToString(sum[:4])
}
