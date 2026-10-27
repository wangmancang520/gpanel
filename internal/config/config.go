// Package config holds process-level settings for the panel.
package config

import (
	"flag"
	"os"
	"path/filepath"
)

type Config struct {
	Addr    string // HTTP listen address
	DataDir string // all persistent state lives under here
	WebDir  string // optional: serve the frontend from disk instead of the embedded copy (development)
	// JavaMirror optionally downloads Temurin builds from a mirror, with placeholders
	// {major} {image} {arch} {os} {file}; falls back to the official source.
	JavaMirror string
	// DisableTLS serves plain HTTP only, ignoring the saved HTTPS settings;
	// a way back in if a certificate or "force HTTPS" locks you out.
	DisableTLS bool
}

// Load reads flags, falling back to GPANEL_* environment variables, then defaults.
func Load() Config {
	var c Config
	flag.StringVar(&c.Addr, "addr", env("GPANEL_ADDR", "127.0.0.1:24444"), "listen address")
	flag.StringVar(&c.DataDir, "data", env("GPANEL_DATA", "data"), "data directory")
	flag.StringVar(&c.WebDir, "web-dir", env("GPANEL_WEB_DIR", ""), "serve frontend from this directory (dev)")
	flag.StringVar(&c.JavaMirror, "java-mirror", env("GPANEL_JAVA_MIRROR", ""), "Adoptium mirror URL template, e.g. https://mirrors.tuna.tsinghua.edu.cn/Adoptium/{major}/{image}/{arch}/{os}/{file}")
	flag.BoolVar(&c.DisableTLS, "disable-tls", os.Getenv("GPANEL_DISABLE_TLS") == "1", "serve plain HTTP only, ignoring HTTPS settings (recovery)")
	flag.Parse()
	if abs, err := filepath.Abs(c.DataDir); err == nil {
		c.DataDir = abs
	}
	return c
}

// Path joins elem onto the data directory.
func (c Config) Path(elem ...string) string {
	return filepath.Join(append([]string{c.DataDir}, elem...)...)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
