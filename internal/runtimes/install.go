package runtimes

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"
)

type JobState string

const (
	JobDownloading JobState = "downloading"
	JobVerifying   JobState = "verifying"
	JobExtracting  JobState = "extracting"
	JobDone        JobState = "done"
	JobFailed      JobState = "failed"
	JobCancelled   JobState = "cancelled"
)

const finishedJobTTL = time.Hour

// Job is the API view of an install in progress (or recently finished).
type Job struct {
	ID         string     `json:"id"`
	Release    Release    `json:"release"`
	State      JobState   `json:"state"`
	Source     string     `json:"source,omitempty"` // "mirror" / "official"
	Downloaded int64      `json:"downloaded"`
	Total      int64      `json:"total"`
	Error      string     `json:"error,omitempty"`
	RuntimeID  string     `json:"runtimeId,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

type job struct {
	Job        // guarded by Registry.jobsMu, except downloaded
	downloaded atomic.Int64
	cancel     context.CancelFunc
}

func (r *Registry) installer(kind string) (Installer, error) {
	inst, ok := r.provider(kind).(Installer)
	if !ok {
		return nil, fmt.Errorf("运行时类型 %s 不支持在线安装", kind)
	}
	return inst, nil
}

// Releases lists installable releases of kind, marking those already installed.
func (r *Registry) Releases(ctx context.Context, kind string) ([]Release, error) {
	inst, err := r.installer(kind)
	if err != nil {
		return nil, err
	}
	rels, err := inst.Releases(ctx)
	if err != nil {
		return nil, err
	}
	out := slices.Clone(rels)
	for i := range out {
		_, err := os.Stat(filepath.Join(inst.InstallDir(), out[i].ID))
		out[i].Installed = err == nil
	}
	return out, nil
}

// Install starts downloading a release in the background and returns its job.
// The release is looked up by ID from the provider's own list, so clients
// can never make the panel download an arbitrary URL.
func (r *Registry) Install(ctx context.Context, kind, releaseID string) (Job, error) {
	inst, err := r.installer(kind)
	if err != nil {
		return Job{}, err
	}
	rels, err := inst.Releases(ctx)
	if err != nil {
		return Job{}, err
	}
	i := slices.IndexFunc(rels, func(rel Release) bool { return rel.ID == releaseID })
	if i < 0 {
		return Job{}, errors.New("找不到该版本，请刷新列表")
	}
	rel := rels[i]
	if _, err := os.Stat(filepath.Join(inst.InstallDir(), rel.ID)); err == nil {
		return Job{}, errors.New("该版本已安装")
	}

	r.jobsMu.Lock()
	defer r.jobsMu.Unlock()
	for _, j := range r.jobs {
		if j.Release.ID == rel.ID && j.FinishedAt == nil {
			return Job{}, errors.New("该版本正在安装中")
		}
	}
	jctx, cancel := context.WithCancel(context.Background())
	j := &job{Job: Job{ID: randomID(), Release: rel, State: JobDownloading, Total: rel.Size, StartedAt: time.Now()}, cancel: cancel}
	r.jobs[j.ID] = j
	go r.runInstall(jctx, inst, j)
	return r.snapshotLocked(j), nil
}

func (r *Registry) Jobs() []Job {
	r.jobsMu.Lock()
	defer r.jobsMu.Unlock()
	out := make([]Job, 0, len(r.jobs))
	for id, j := range r.jobs {
		if j.FinishedAt != nil && time.Since(*j.FinishedAt) > finishedJobTTL {
			delete(r.jobs, id)
			continue
		}
		out = append(out, r.snapshotLocked(j))
	}
	slices.SortFunc(out, func(a, b Job) int { return b.StartedAt.Compare(a.StartedAt) })
	return out
}

func (r *Registry) CancelJob(id string) error {
	r.jobsMu.Lock()
	defer r.jobsMu.Unlock()
	j, ok := r.jobs[id]
	if !ok {
		return errors.New("任务不存在")
	}
	if j.FinishedAt != nil {
		return errors.New("任务已结束")
	}
	j.cancel()
	return nil
}

func (r *Registry) snapshotLocked(j *job) Job {
	s := j.Job
	s.Downloaded = j.downloaded.Load()
	return s
}

func (r *Registry) setJob(j *job, fn func(*Job)) {
	r.jobsMu.Lock()
	fn(&j.Job)
	r.jobsMu.Unlock()
}

func (r *Registry) runInstall(ctx context.Context, inst Installer, j *job) {
	defer j.cancel()
	runtimeID, err := r.install(ctx, inst, j)
	now := time.Now()
	r.setJob(j, func(s *Job) {
		s.FinishedAt = &now
		switch {
		case err == nil:
			s.State, s.RuntimeID = JobDone, runtimeID
		case ctx.Err() != nil:
			s.State, s.Error = JobCancelled, "已取消"
		default:
			s.State, s.Error = JobFailed, err.Error()
		}
	})
	if err != nil && ctx.Err() == nil {
		log.Printf("安装 %s 失败: %v", j.Release.ID, err)
	}
}

func (r *Registry) install(ctx context.Context, inst Installer, j *job) (string, error) {
	rel := j.Release
	dir := inst.InstallDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	sum, err := r.download(ctx, rel, tmp, j)
	if err != nil {
		return "", err
	}

	r.setJob(j, func(s *Job) { s.State = JobVerifying })
	if !strings.EqualFold(sum, rel.SHA256) {
		return "", fmt.Errorf("SHA-256 校验失败：期望 %s，实际 %s", rel.SHA256, sum)
	}

	r.setJob(j, func(s *Job) { s.State = JobExtracting })
	staging, err := os.MkdirTemp(dir, ".staging-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	if err := extractArchive(tmp, rel.FileName, staging); err != nil {
		return "", fmt.Errorf("解压失败: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	// Archives normally hold a single top-level directory (jdk-21.0.x+y/).
	top := staging
	if entries, err := os.ReadDir(staging); err == nil && len(entries) == 1 && entries[0].IsDir() {
		top = filepath.Join(staging, entries[0].Name())
	}
	dest := filepath.Join(dir, rel.ID)
	if err := os.Rename(top, dest); err != nil {
		return "", err
	}

	r.Scan(context.Background())
	destReal, _ := filepath.EvalSymlinks(dest)
	for _, rt := range r.List(rel.Kind) {
		if rt.Home == destReal || strings.HasPrefix(rt.Home, destReal+string(filepath.Separator)) {
			return rt.ID, nil
		}
	}
	return "", errors.New("已解压，但未识别到可用的运行时")
}

// download fetches the release (mirror first, then official) into dst,
// reporting progress on j and returning the SHA-256 of what was written.
func (r *Registry) download(ctx context.Context, rel Release, dst *os.File, j *job) (string, error) {
	type source struct{ name, url string }
	var sources []source
	if rel.MirrorURL != "" {
		sources = append(sources, source{"mirror", rel.MirrorURL})
	}
	sources = append(sources, source{"official", rel.URL})

	var lastErr error
	for _, src := range sources {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if _, err := dst.Seek(0, io.SeekStart); err != nil {
			return "", err
		}
		if err := dst.Truncate(0); err != nil {
			return "", err
		}
		j.downloaded.Store(0)
		r.setJob(j, func(s *Job) { s.Source = src.name })

		sum, err := fetch(ctx, src.url, rel.Size, dst, &j.downloaded)
		if err == nil {
			return sum, nil
		}
		lastErr = err
		if src.name == "mirror" {
			log.Printf("镜像下载 %s 失败，改用官方源: %v", rel.FileName, err)
		}
	}
	return "", lastErr
}

func fetch(ctx context.Context, url string, size int64, dst io.Writer, progress *atomic.Int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载失败: %s", resp.Status)
	}
	limit := int64(2 << 30) // refuse absurdly large bodies
	if size > 0 {
		limit = size + 1
	}
	h := sha256.New()
	w := io.MultiWriter(dst, h, progressWriter{progress})
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit))
	if err != nil {
		return "", err
	}
	if size > 0 && n != size {
		return "", fmt.Errorf("文件大小不符：期望 %d，实际 %d", size, n)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type progressWriter struct{ n *atomic.Int64 }

func (p progressWriter) Write(b []byte) (int, error) {
	p.n.Add(int64(len(b)))
	return len(b), nil
}

// Uninstall removes a panel-managed runtime that no instance uses.
func (r *Registry) Uninstall(id string) error {
	rt, ok := r.Get(id)
	if !ok {
		return errors.New("运行时不存在")
	}
	if !rt.Managed {
		return errors.New("只能卸载由面板安装的运行时")
	}
	if r.InUse != nil {
		if names := r.InUse(id); len(names) > 0 {
			return fmt.Errorf("以下实例正在使用该运行时：%s", strings.Join(names, "、"))
		}
	}
	inst, err := r.installer(rt.Kind)
	if err != nil {
		return err
	}
	root, err := filepath.EvalSymlinks(inst.InstallDir())
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, rt.Home)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return errors.New("运行时不在面板管理目录中")
	}
	top := strings.Split(rel, string(filepath.Separator))[0]
	if err := os.RemoveAll(filepath.Join(root, top)); err != nil {
		return err
	}
	r.Scan(context.Background())
	return nil
}

func randomID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
