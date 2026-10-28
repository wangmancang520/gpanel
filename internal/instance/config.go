package instance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Config is the persisted definition of a game instance.
type Config struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	WorkDir     string            `json:"workDir"`     // absolute, user-chosen; empty = <data>/instances/<id> (resolved at runtime so the data dir can move)
	Command     string            `json:"command"`     // e.g. "{{java}} -Xmx4G -jar server.jar nogui"
	StopCommand string            `json:"stopCommand"` // written to the console to stop gracefully; CtrlC sends Ctrl+C; empty = SIGTERM
	StopTimeout int               `json:"stopTimeout"` // seconds before escalating to SIGTERM / SIGKILL
	Env         map[string]string `json:"env"`
	Runtime     string            `json:"runtime"` // runtime ID (e.g. a Java install); empty = system default
	AutoStart   bool              `json:"autoStart"`
	AutoRestart bool              `json:"autoRestart"`

	// Extensions holds per-feature settings owned by other modules
	// (schedules, backups, ...), keyed by module name, so adding a feature
	// never requires changing this struct.
	Extensions map[string]json.RawMessage `json:"extensions,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
}

func (c *Config) normalize() error {
	c.Name = strings.TrimSpace(c.Name)
	c.Command = strings.TrimSpace(c.Command)
	if c.Name == "" {
		return errors.New("名称不能为空")
	}
	if c.Command == "" {
		return errors.New("启动命令不能为空")
	}
	if _, err := splitCommand(c.Command); err != nil {
		return err
	}
	if c.StopTimeout <= 0 {
		c.StopTimeout = 30
	}
	return nil
}

type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateCrashed  State = "crashed"
)

// Status is a point-in-time view of an instance for the API.
type Status struct {
	Config
	Dir       string     `json:"dir"` // effective working directory
	State     State      `json:"state"`
	Pid       int        `json:"pid,omitempty"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	ExitCode  *int       `json:"exitCode,omitempty"`
	LastError string     `json:"lastError,omitempty"`
}

// LaunchSpec is what a launch is assembled from. LaunchHooks fill it in.
type LaunchSpec struct {
	// Vars are substituted into the command as {{name}}.
	Vars map[string]string
	// Env is merged over the panel's environment and the instance's own Env.
	Env map[string]string
	// PathPrepend directories are put in front of PATH.
	PathPrepend []string
}

// LaunchHook lets other modules (Java runtimes, Node, Steam, ...) contribute
// to an instance's launch without the instance package knowing about them.
type LaunchHook func(cfg Config, spec *LaunchSpec) error

var placeholder = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.-]+)\s*\}\}`)

// buildArgv expands placeholders and returns argv plus the final environment.
func buildArgv(cfg Config, spec *LaunchSpec) ([]string, []string, error) {
	words, err := splitCommand(cfg.Command)
	if err != nil {
		return nil, nil, err
	}
	for i, w := range words {
		var missing string
		words[i] = placeholder.ReplaceAllStringFunc(w, func(m string) string {
			name := placeholder.FindStringSubmatch(m)[1]
			v, ok := spec.Vars[name]
			if !ok {
				missing = name
			}
			return v
		})
		if missing != "" {
			return nil, nil, fmt.Errorf("命令中的变量 {{%s}} 未定义", missing)
		}
	}

	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	env["TERM"] = "xterm-256color"
	for k, v := range cfg.Env {
		env[k] = v
	}
	for k, v := range spec.Env {
		env[k] = v
	}
	if len(spec.PathPrepend) > 0 {
		sep := ":"
		if runtime.GOOS == "windows" {
			sep = ";"
		}
		env["PATH"] = strings.Join(append(append([]string{}, spec.PathPrepend...), env["PATH"]), sep)
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	envList := make([]string, 0, len(keys))
	for _, k := range keys {
		envList = append(envList, k+"="+env[k])
	}
	return words, envList, nil
}

// CtrlC as a StopCommand sends Ctrl+C to the console instead of a line.
const CtrlC = "^C"

var plainArg = regexp.MustCompile(`^[A-Za-z0-9_./:=+,@%-]+$`)

// QuoteArg quotes s so that splitCommand reads it back as exactly one
// argument, e.g. for building `bash -c '<script>'`.
func QuoteArg(s string) string {
	if plainArg.MatchString(s) {
		return s
	}
	// Single quotes keep everything literal; a quote itself is closed,
	// backslash-escaped and reopened.
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// splitCommand splits a command line shell-style (quotes and backslash
// escapes) without invoking a shell. Use `bash start.sh` for shell features.
func splitCommand(s string) ([]string, error) {
	var (
		words   []string
		cur     strings.Builder
		inWord  bool
		quote   rune
		escaped bool
	)
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped, inWord = true, true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, inWord = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 || escaped {
		return nil, errors.New("启动命令引号不匹配")
	}
	if inWord {
		words = append(words, cur.String())
	}
	if len(words) == 0 {
		return nil, errors.New("启动命令不能为空")
	}
	return words, nil
}
