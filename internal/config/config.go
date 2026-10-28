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

	// One-shot commands for the installer and admins; the panel exits after.
	ShowVersion   bool   // -version
	ResetPassword bool   // -reset-password [-user NAME]
	User          string // account for -reset-password
	PrintUnit     bool   // -print-systemd-unit
	PrintEnv      bool   // -print-env-template

	// -import-gsmanager DIR [-dry-run] [-move-list FILE]: import instances
	// from a native GSManager 3 install (run with the panel stopped).
	ImportGSManager string
	DryRun          bool
	MoveList        string // file with one work dir per line to move into the data dir
	RewriteJava     bool   // replace hard-coded GSManager Java paths in start scripts
}

// Load reads flags, falling back to GPANEL_* environment variables, then defaults.
func Load() Config {
	var c Config
	flag.StringVar(&c.Addr, "addr", env("GPANEL_ADDR", "127.0.0.1:24444"), "listen address")
	flag.StringVar(&c.DataDir, "data", env("GPANEL_DATA", "data"), "data directory")
	flag.StringVar(&c.WebDir, "web-dir", env("GPANEL_WEB_DIR", ""), "serve frontend from this directory (dev)")
	flag.StringVar(&c.JavaMirror, "java-mirror", env("GPANEL_JAVA_MIRROR", ""), "Adoptium mirror URL template, e.g. https://mirrors.tuna.tsinghua.edu.cn/Adoptium/{major}/{image}/{arch}/{os}/{file}")
	flag.BoolVar(&c.DisableTLS, "disable-tls", os.Getenv("GPANEL_DISABLE_TLS") == "1", "serve plain HTTP only, ignoring HTTPS settings (recovery)")
	flag.BoolVar(&c.ShowVersion, "version", false, "print the version and exit")
	flag.BoolVar(&c.ResetPassword, "reset-password", false, "set a new random password for -user, print it and exit")
	flag.StringVar(&c.User, "user", "admin", "account for -reset-password")
	flag.BoolVar(&c.PrintUnit, "print-systemd-unit", false, "print the systemd unit for this version and exit")
	flag.BoolVar(&c.PrintEnv, "print-env-template", false, "print the environment file template and exit")
	flag.StringVar(&c.ImportGSManager, "import-gsmanager", "", "import instances from the GSManager install in this directory and exit (stop the panel first)")
	flag.BoolVar(&c.DryRun, "dry-run", false, "with -import-gsmanager: only report what would be imported")
	flag.StringVar(&c.MoveList, "move-list", "", "with -import-gsmanager: file listing work dirs to move into the data dir")
	flag.BoolVar(&c.RewriteJava, "rewrite-java", false, "with -import-gsmanager: make start scripts that call GSManager's JDK use the instance's runtime (backups kept)")
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
