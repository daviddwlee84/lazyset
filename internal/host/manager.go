// Package host discovers and launches tools on the local machine or an OpenSSH
// destination. Discovery never starts a TUI or prompts for authentication.
package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/lazyset/internal/core"
	"github.com/daviddwlee84/lazyset/internal/session"
)

const (
	Authentication = "authentication"
	Offline        = "offline"
	Configuration  = "config"
	Discovery      = "discovery"
)

// Error distinguishes an unknown observation from an absent executable.
type Error struct {
	Kind   string
	HostID string
	Detail string
	Err    error
}

func (e *Error) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("host %s: %s", e.HostID, e.Detail)
	}
	return fmt.Sprintf("host %s: %s", e.HostID, e.Kind)
}
func (e *Error) Unwrap() error { return e.Err }
func IsAuthentication(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == Authentication
}

// Manager owns only its private fallback SSH masters. User-configured masters
// remain shared and are never terminated by Close.
type Manager struct {
	mu           sync.Mutex
	operations   sync.WaitGroup
	sshBinary    string
	probeTimeout time.Duration
	policies     map[string]sshPolicy
	controlDir   string
	closed       bool
}

func NewManager() *Manager {
	return &Manager{sshBinary: "ssh", probeTimeout: 8 * time.Second, policies: make(map[string]sshPolicy)}
}

func (m *Manager) Discover(ctx context.Context, h core.Host, tools []core.Tool) ([]core.Availability, error) {
	if err := m.begin(); err != nil {
		return unknown(tools, err.Error()), err
	}
	defer m.operations.Done()
	if h.SSH == "" {
		return discoverLocal(ctx, h, tools)
	}
	return m.discoverRemote(ctx, h, tools)
}

// LaunchSpec preserves the tool's argv; shell parsing occurs only on the SSH
// side, where every value is quoted as a single POSIX-shell word.
func (m *Manager) LaunchSpec(h core.Host, t core.Tool, resolvedPath string) (session.Spec, error) {
	if err := m.begin(); err != nil {
		return session.Spec{}, err
	}
	defer m.operations.Done()
	if len(t.Command) == 0 || t.Command[0] == "" {
		return session.Spec{}, errors.New("tool command is empty")
	}
	argv := append([]string(nil), t.Command...)
	if resolvedPath != "" {
		argv[0] = resolvedPath
	}
	env := mergedEnv(h.Env, t.Env)
	if err := validateInvocation(argv, t.Dir, env); err != nil {
		return session.Spec{}, err
	}
	if h.SSH == "" {
		dir, err := absoluteDir(t.Dir, env)
		if err != nil {
			return session.Spec{}, err
		}
		return session.Spec{Command: argv, Dir: dir, Env: env}, nil
	}
	p, err := m.policy(context.Background(), h)
	if err != nil {
		return session.Spec{}, err
	}
	remote, err := remoteInvocation(argv, t.Dir, env)
	if err != nil {
		return session.Spec{}, err
	}
	args := []string{"-tt", "-o", "EscapeChar=none", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "RemoteCommand=none"}
	args = append(args, p.options()...)
	args = append(args, "--", h.SSH, remote)
	return session.Spec{Command: append([]string{m.sshBinary}, args...)}, nil
}

// ConnectCommand is intended for a native-terminal handoff: SSH owns passwords,
// MFA and host-key trust. It must not be run inside an embedded terminal.
func (m *Manager) ConnectCommand(ctx context.Context, h core.Host) (*exec.Cmd, error) {
	if h.SSH == "" {
		return nil, errors.New("local host does not require SSH authentication")
	}
	p, err := m.policy(ctx, h)
	if err != nil {
		return nil, err
	}
	args := []string{"-T", "-o", "BatchMode=no", "-o", "RemoteCommand=none"}
	args = append(args, p.options()...)
	if p.owned {
		// -f backgrounds only after authentication. The foreground native SSH
		// process can therefore prompt while Bubble Tea has released the TTY.
		args = append(args, "-N", "-f", "--", h.SSH)
	} else {
		// Respect the user's ControlMaster/ControlPersist lifetime. When their
		// policy disables reuse, this verifies authentication but cannot cache it.
		args = append(args, "--", h.SSH, "true")
	}
	return exec.CommandContext(ctx, m.sshBinary, args...), nil
}

func (m *Manager) begin() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("host manager is closed")
	}
	m.operations.Add(1)
	return nil
}

func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.mu.Unlock()
	m.operations.Wait()
	m.mu.Lock()
	var owned []sshPolicy
	for _, p := range m.policies {
		if p.owned {
			owned = append(owned, p)
		}
	}
	dir := m.controlDir
	m.mu.Unlock()
	var errs []error
	for _, p := range owned {
		// No socket means no master was ever started, or SSH already exited.
		if _, err := os.Stat(p.path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		cmd := exec.CommandContext(ctx, m.sshBinary, "-o", "BatchMode=yes", "-S", p.path, "-O", "exit", "--", p.destination)
		cmd.WaitDelay = 200 * time.Millisecond
		var output limitedBuffer
		cmd.Stdout, cmd.Stderr = &output, &output
		err := cmd.Run()
		cancel()
		if err != nil {
			errs = append(errs, fmt.Errorf("close owned SSH master: %w", err))
		}
	}
	if dir != "" {
		if err := os.RemoveAll(dir); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func mergedEnv(host, tool map[string]string) map[string]string {
	result := make(map[string]string, len(host)+len(tool))
	for k, v := range host {
		result[k] = v
	}
	for k, v := range tool {
		result[k] = v
	}
	return result
}

func envSlice(overrides map[string]string) []string {
	env := make(map[string]string)
	for _, s := range os.Environ() {
		if k, v, ok := strings.Cut(s, "="); ok {
			env[k] = v
		}
	}
	for k, v := range overrides {
		env[k] = v
	}
	keys := sortedKeys(env)
	result := make([]string, 0, len(keys))
	for _, k := range keys {
		result = append(result, k+"="+env[k])
	}
	return result
}

func sortedKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func validEnvKey(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if c != '_' && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func validateInvocation(argv []string, dir string, env map[string]string) error {
	for _, arg := range append(append([]string(nil), argv...), dir) {
		if strings.ContainsRune(arg, 0) {
			return errors.New("command or directory contains NUL")
		}
	}
	for k, v := range env {
		if !validEnvKey(k) || strings.ContainsRune(v, 0) {
			return fmt.Errorf("invalid environment variable %q", k)
		}
	}
	return nil
}

func unknown(tools []core.Tool, reason string) []core.Availability {
	rows := make([]core.Availability, len(tools))
	for i, t := range tools {
		rows[i] = core.Availability{ToolID: t.ID, State: "unknown", Reason: reason}
	}
	return rows
}

func candidates(t core.Tool) []string {
	if len(t.Candidates) > 0 {
		return t.Candidates
	}
	if len(t.Command) > 0 {
		return t.Command[:1]
	}
	return nil
}

func supported(t core.Tool, platform string) bool {
	if len(t.Platforms) == 0 {
		return true
	}
	for _, p := range t.Platforms {
		if p == platform {
			return true
		}
	}
	return false
}

func homeRelativeDir(dir string) bool {
	return dir == "~" || strings.HasPrefix(dir, "~/")
}

func absoluteDir(dir string, env map[string]string) (string, error) {
	if dir == "" {
		return os.Getwd()
	}
	if homeRelativeDir(dir) {
		home, ok := env["HOME"]
		if !ok {
			var err error
			home, err = os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("resolve home directory: %w", err)
			}
		}
		if home == "" {
			return "", errors.New("HOME is empty; cannot resolve home directory")
		}
		dir = home + strings.TrimPrefix(dir, "~")
	}
	return filepath.Abs(dir)
}

// limitedBuffer prevents banners or a faulty probe from allocating unbounded
// memory. It keeps the beginning; discovery treats a truncated frame as unknown.
type limitedBuffer struct {
	data      []byte
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	const limit = 1024 * 1024
	remaining := limit - len(b.data)
	if len(p) > remaining {
		b.truncated = true
		p = p[:remaining]
	}
	b.data = append(b.data, p...)
	return n, nil
}
func (b *limitedBuffer) String() string { return string(b.data) }
