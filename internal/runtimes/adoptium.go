package runtimes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	adoptiumAPI   = "https://api.adoptium.net/v3"
	releasesTTL   = time.Hour
	adoptiumImage = "hotspot"
	// Some mirrors (e.g. TUNA) reject Go's default User-Agent.
	userAgent = "gpanel"
)

// Adoptium lists Eclipse Temurin builds for this OS/architecture.
type Adoptium struct {
	// Mirror optionally rewrites downloads to a mirror, e.g.
	// https://mirrors.tuna.tsinghua.edu.cn/Adoptium/{major}/{image}/{arch}/{os}/{file}
	// Mirrors can lag behind; Download falls back to the official URL.
	Mirror string

	Client *http.Client

	mu       sync.Mutex
	cache    []Release
	cachedAt time.Time
}

func (a *Adoptium) client() *http.Client {
	if a.Client != nil {
		return a.Client
	}
	return http.DefaultClient
}

// Releases returns the latest JDK and JRE for every available feature
// version, cached for an hour.
func (a *Adoptium) Releases(ctx context.Context) ([]Release, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cache != nil && time.Since(a.cachedAt) < releasesTTL {
		return a.cache, nil
	}

	var info struct {
		AvailableReleases    []int `json:"available_releases"`
		AvailableLTSReleases []int `json:"available_lts_releases"`
	}
	if err := a.getJSON(ctx, adoptiumAPI+"/info/available_releases", &info); err != nil {
		return nil, err
	}
	osName, arch := adoptiumPlatform()

	type result struct {
		rels []Release
		err  error
	}
	results := make([]result, len(info.AvailableReleases))
	var wg sync.WaitGroup
	for i, major := range info.AvailableReleases {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rels, err := a.latest(ctx, major, osName, arch, slices.Contains(info.AvailableLTSReleases, major))
			results[i] = result{rels, err}
		}()
	}
	wg.Wait()

	var out []Release
	for _, r := range results {
		if r.err != nil {
			return nil, r.err
		}
		out = append(out, r.rels...)
	}
	slices.SortFunc(out, func(x, y Release) int {
		if x.Major != y.Major {
			return y.Major - x.Major
		}
		return strings.Compare(x.ImageType, y.ImageType)
	})
	a.cache, a.cachedAt = out, time.Now()
	return out, nil
}

type adoptiumAsset struct {
	ReleaseName string `json:"release_name"`
	Binary      struct {
		ImageType string `json:"image_type"`
		Package   struct {
			Name     string `json:"name"`
			Link     string `json:"link"`
			Checksum string `json:"checksum"`
			Size     int64  `json:"size"`
		} `json:"package"`
	} `json:"binary"`
	Version struct {
		Major  int    `json:"major"`
		Semver string `json:"semver"`
	} `json:"version"`
}

func (a *Adoptium) latest(ctx context.Context, major int, osName, arch string, lts bool) ([]Release, error) {
	q := url.Values{"os": {osName}, "architecture": {arch}, "vendor": {"eclipse"}}
	var assets []adoptiumAsset
	u := fmt.Sprintf("%s/assets/latest/%d/%s?%s", adoptiumAPI, major, adoptiumImage, q.Encode())
	if err := a.getJSON(ctx, u, &assets); err != nil {
		return nil, err
	}
	var out []Release
	for _, as := range assets {
		img := as.Binary.ImageType
		pkg := as.Binary.Package
		if (img != "jdk" && img != "jre") || pkg.Link == "" || pkg.Checksum == "" {
			continue
		}
		out = append(out, Release{
			ID:        as.ReleaseName + "-" + img,
			Kind:      "java",
			Vendor:    "Eclipse Temurin",
			Version:   strings.TrimSuffix(strings.TrimSuffix(as.Version.Semver, ".LTS"), ".0"),
			Major:     major,
			ImageType: img,
			LTS:       lts,
			FileName:  pkg.Name,
			URL:       pkg.Link,
			MirrorURL: a.mirrorURL(major, img, arch, osName, pkg.Name),
			SHA256:    pkg.Checksum,
			Size:      pkg.Size,
		})
	}
	return out, nil
}

func (a *Adoptium) mirrorURL(major int, image, arch, osName, file string) string {
	if a.Mirror == "" {
		return ""
	}
	return strings.NewReplacer(
		"{major}", fmt.Sprint(major), "{image}", image, "{arch}", arch, "{os}", osName, "{file}", file,
	).Replace(a.Mirror)
}

func (a *Adoptium) getJSON(ctx context.Context, u string, v any) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := a.client().Do(req)
	if err != nil {
		return fmt.Errorf("无法访问 Adoptium API: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Adoptium API 返回 %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// adoptiumPlatform maps Go's GOOS/GOARCH to Adoptium's naming.
func adoptiumPlatform() (osName, arch string) {
	switch runtime.GOOS {
	case "darwin":
		osName = "mac"
	case "linux":
		osName = "linux"
		if musl, _ := filepath.Glob("/lib/ld-musl-*"); len(musl) > 0 {
			osName = "alpine-linux"
		}
	default:
		osName = runtime.GOOS
	}
	switch runtime.GOARCH {
	case "amd64":
		arch = "x64"
	case "arm64":
		arch = "aarch64"
	case "386":
		arch = "x32"
	default:
		arch = runtime.GOARCH // arm, ppc64le, s390x, riscv64
	}
	return osName, arch
}
