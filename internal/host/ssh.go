package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/lazyset/internal/core"
)

type sshPolicy struct {
	destination string
	owned       bool
	path        string
}

func (p sshPolicy) options() []string {
	if !p.owned {
		return nil
	}
	return []string{"-o", "ControlMaster=auto", "-o", "ControlPersist=600", "-o", "ControlPath=" + p.path}
}

func validateDestination(destination string) error {
	if destination == "" || strings.HasPrefix(destination, "-") || strings.ContainsAny(destination, "\x00\r\n\t ") {
		return errors.New("SSH destination must be a single host alias or user@host, not a command")
	}
	return nil
}

func (m *Manager) policy(ctx context.Context, h core.Host) (sshPolicy, error) {
	if err := validateDestination(h.SSH); err != nil {
		return sshPolicy{}, &Error{Kind: Configuration, HostID: h.ID, Detail: err.Error(), Err: err}
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return sshPolicy{}, errors.New("host manager is closed")
	}
	if p, ok := m.policies[h.SSH]; ok {
		m.mu.Unlock()
		return p, nil
	}
	m.mu.Unlock()
	probeCtx, cancel := context.WithTimeout(ctx, m.probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, m.sshBinary, "-G", "--", h.SSH)
	cmd.WaitDelay = 200 * time.Millisecond
	var stdout, stderr limitedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return sshPolicy{}, &Error{Kind: Configuration, HostID: h.ID, Detail: "cannot inspect SSH configuration: " + shortDiagnostic(stderr.String(), err), Err: err}
	}
	settings := make(map[string]string)
	for _, line := range strings.Split(stdout.String(), "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			settings[key] = strings.TrimSpace(value)
		}
	}
	if _, ok := settings["hostname"]; !ok {
		return sshPolicy{}, &Error{Kind: Configuration, HostID: h.ID, Detail: "ssh -G did not return an effective hostname"}
	}
	// -G exposes effective values, not whether a default-valued directive was
	// explicitly written. Any effective sharing configuration is left intact.
	shared := nonDefault(settings["controlpath"]) || nonDefault(settings["controlmaster"]) || nonDefault(settings["controlpersist"])
	p := sshPolicy{destination: h.SSH, owned: !shared}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return sshPolicy{}, errors.New("host manager is closed")
	}
	if existing, ok := m.policies[h.SSH]; ok {
		return existing, nil
	}
	if p.owned {
		if m.controlDir == "" {
			dir, err := os.MkdirTemp("", "ls-ssh-")
			if err != nil {
				return sshPolicy{}, err
			}
			// Unix-domain sockets have a short pathname limit (104 on macOS).
			// A private /tmp directory is a fallback for unusually long TMPDIRs.
			if len(dir) > 75 {
				_ = os.Remove(dir)
				dir, err = os.MkdirTemp("/tmp", "ls-ssh-")
				if err != nil {
					return sshPolicy{}, err
				}
			}
			m.controlDir = dir
		}
		hash := sha256.Sum256([]byte(h.SSH))
		p.path = filepath.Join(m.controlDir, hex.EncodeToString(hash[:6])+".sock")
	}
	m.policies[h.SSH] = p
	return p, nil
}

func nonDefault(value string) bool {
	switch strings.ToLower(value) {
	case "", "none", "no", "false", "0":
		return false
	}
	return true
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func environmentScript(env map[string]string) string {
	var b strings.Builder
	for _, key := range sortedKeys(env) {
		fmt.Fprintf(&b, "export %s=%s\n", key, shellQuote(env[key]))
	}
	return b.String()
}

func remoteInvocation(argv []string, dir string, env map[string]string) (string, error) {
	if err := validateInvocation(argv, dir, env); err != nil {
		return "", err
	}
	if len(argv) == 0 || argv[0] == "" {
		return "", errors.New("tool command is empty")
	}
	var b strings.Builder
	b.WriteString(environmentScript(env))
	b.WriteString(directoryScript(dir, "printf '%s\\n' 'working directory is unavailable' >&2; exit 125"))
	b.WriteString("exec ")
	for i, arg := range argv {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(shellQuote(arg))
	}
	return "exec sh -c " + shellQuote(b.String()), nil
}

func shellDir(dir string) string {
	if strings.HasPrefix(dir, "-") {
		return "./" + dir
	}
	return dir
}

// directoryScript expands only the home shorthand. HOME and every suffix stay
// quoted data, even when they contain spaces, quotes, or command substitutions.
// The caller exports the effective environment before using this script.
func directoryScript(dir, failure string) string {
	if dir == "" {
		return ""
	}
	var b strings.Builder
	target := shellQuote(shellDir(dir))
	if homeRelativeDir(dir) {
		fmt.Fprintf(&b, "[ -n \"$HOME\" ] || { %s; }\n", failure)
		b.WriteString("case \"$HOME\" in\n/*) _ls_dir=\"$HOME\" ;;\n*) _ls_dir=\"./$HOME\" ;;\nesac\n")
		target = "\"$_ls_dir\"" + shellQuote(strings.TrimPrefix(dir, "~"))
	}
	fmt.Fprintf(&b, "cd %s 2>/dev/null || { %s; }\n", target, failure)
	return b.String()
}

func (m *Manager) discoverRemote(ctx context.Context, h core.Host, tools []core.Tool) ([]core.Availability, error) {
	p, err := m.policy(ctx, h)
	if err != nil {
		return unknown(tools, err.Error()), err
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return unknown(tools, err.Error()), err
	}
	token := "lazyset:" + hex.EncodeToString(tokenBytes)
	script, err := discoveryScript(h, tools, token)
	if err != nil {
		return unknown(tools, err.Error()), err
	}
	probeCtx, cancel := context.WithTimeout(ctx, m.probeTimeout)
	defer cancel()
	args := []string{"-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=5", "-o", "RemoteCommand=none"}
	args = append(args, p.options()...)
	args = append(args, "--", h.SSH, "exec sh -c "+shellQuote(script))
	cmd := exec.CommandContext(probeCtx, m.sshBinary, args...)
	cmd.WaitDelay = 200 * time.Millisecond
	var stdout, stderr limitedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if err != nil {
		e := classifySSHError(h.ID, stderr.String(), err, probeCtx.Err())
		return unknown(tools, e.Detail), e
	}
	rows, err := parseDiscovery(stdout.data, token, tools)
	if err != nil || stdout.truncated {
		if err == nil {
			err = errors.New("SSH discovery output exceeded its size limit")
		}
		e := &Error{Kind: Discovery, HostID: h.ID, Detail: "cannot read SSH discovery: " + err.Error(), Err: err}
		return unknown(tools, e.Detail), e
	}
	return rows, nil
}

func discoveryScript(h core.Host, tools []core.Tool, token string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "printf '\\000%%s\\000' %s\n", shellQuote(token+":begin"))
	b.WriteString("_ls_platform=$(uname -s 2>/dev/null)\n_ls_sleep=$(command -v sleep 2>/dev/null)\nprintf '%s\\000' \"$_ls_platform\"\n")
	for i, t := range tools {
		env := mergedEnv(h.Env, t.Env)
		if err := validateInvocation(candidates(t), t.Dir, env); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "(\n%s", environmentScript(env))
		failure := fmt.Sprintf("printf '%%s\\000' %s unknown '' 'working directory is unavailable'; exit", shellQuote(strconv.Itoa(i)))
		b.WriteString(directoryScript(t.Dir, failure))
		b.WriteString("_ls_state=missing\n_ls_found=''\n_ls_reason='executable is not on PATH'\n_ls_base=$(pwd -P)\n")
		for _, candidate := range candidates(t) {
			// The subshell cannot alter the next tool's PATH or working directory.
			fmt.Fprintf(&b, "if [ \"$_ls_state\" != found ]; then\n_ls_path=$(command -v %s 2>/dev/null)\n", shellQuote(candidate))
			b.WriteString("case \"$_ls_path\" in\n/*) ;;\n*/*) _ls_path=\"$_ls_base/$_ls_path\" ;;\n*) _ls_path='' ;;\nesac\n")
			b.WriteString("if [ -n \"$_ls_path\" ] && [ -f \"$_ls_path\" ] && [ -x \"$_ls_path\" ]; then\n_ls_ok=yes\n")
			if t.ID == "gdu" && filepath.Base(candidate) == "gdu" {
				// This is the only executable-specific probe. GNU coreutils ships
				// a conflicting gdu name. Give the known version command one second.
				b.WriteString("_ls_version=''\nif [ -n \"$_ls_sleep\" ]; then\n_ls_version=$(\n\"$_ls_path\" --version 2>/dev/null &\n_ls_pid=$!\n(\"$_ls_sleep\" 1; kill -KILL \"$_ls_pid\" 2>/dev/null) >/dev/null 2>&1 &\n_ls_timer=$!\nwait \"$_ls_pid\"\n_ls_rc=$?\nkill \"$_ls_timer\" 2>/dev/null\nwait \"$_ls_timer\" 2>/dev/null\nexit \"$_ls_rc\"\n)\nfi\n")
				b.WriteString("case \"$_ls_version\" in\n*coreutils*) _ls_ok=no; _ls_reason='gdu belongs to GNU coreutils, not the disk-usage TUI' ;;\n*'Version:'*'Built time:'*'Built user:'*) ;;\n*) _ls_ok=no; _ls_state=unknown; _ls_reason='cannot verify gdu is the disk-usage TUI' ;;\nesac\n")
			}
			b.WriteString("if [ \"$_ls_ok\" = yes ]; then _ls_state=found; _ls_found=$_ls_path; _ls_reason=''; fi\nfi\nfi\n")
		}
		fmt.Fprintf(&b, "printf '%%s\\000' %s \"$_ls_state\" \"$_ls_found\" \"$_ls_reason\"\n)\n", shellQuote(strconv.Itoa(i)))
	}
	fmt.Fprintf(&b, "printf '\\000%%s\\000' %s\n", shellQuote(token+":end"))
	return b.String(), nil
}

func parseDiscovery(output []byte, token string, tools []core.Tool) ([]core.Availability, error) {
	begin, end := []byte("\x00"+token+":begin\x00"), []byte("\x00"+token+":end\x00")
	start := bytes.Index(output, begin)
	if start < 0 {
		return nil, errors.New("missing discovery frame")
	}
	payload := output[start+len(begin):]
	finish := bytes.Index(payload, end)
	if finish < 0 {
		return nil, errors.New("incomplete discovery frame")
	}
	fields := bytes.Split(payload[:finish], []byte{0})
	if len(fields) != 2+4*len(tools) || len(fields[len(fields)-1]) != 0 {
		return nil, errors.New("invalid discovery record count")
	}
	platform := strings.ToLower(string(fields[0]))
	if platform == "" {
		return nil, errors.New("remote platform could not be detected")
	}
	rows := make([]core.Availability, len(tools))
	for i, t := range tools {
		base := 1 + 4*i
		if string(fields[base]) != strconv.Itoa(i) {
			return nil, errors.New("unexpected discovery record identity")
		}
		row := core.Availability{ToolID: t.ID, State: string(fields[base+1]), Path: string(fields[base+2]), Reason: string(fields[base+3])}
		switch row.State {
		case "found", "missing", "unknown":
		default:
			return nil, errors.New("invalid discovery state")
		}
		if row.State == "found" && !strings.HasPrefix(row.Path, "/") {
			return nil, errors.New("discovery returned a nonabsolute executable")
		}
		if !supported(t, platform) {
			row.State, row.Path, row.Reason = "unsupported", "", "not supported on "+platform
		}
		rows[i] = row
	}
	return rows, nil
}

func classifySSHError(hostID, diagnostic string, err, ctxErr error) *Error {
	kind := Discovery
	lower := strings.ToLower(diagnostic)
	switch {
	case strings.Contains(lower, "permission denied"), strings.Contains(lower, "host key verification failed"), strings.Contains(lower, "remote host identification has changed"), strings.Contains(lower, "no supported authentication"), strings.Contains(lower, "authentication failed"):
		kind = Authentication
	case ctxErr != nil, strings.Contains(lower, "connection refused"), strings.Contains(lower, "connection timed out"), strings.Contains(lower, "could not resolve hostname"), strings.Contains(lower, "no route to host"), strings.Contains(lower, "network is unreachable"), strings.Contains(lower, "connection closed"), strings.Contains(lower, "connection reset"):
		kind = Offline
	}
	if ctxErr != nil {
		err = ctxErr
	}
	return &Error{Kind: kind, HostID: hostID, Detail: shortDiagnostic(diagnostic, err), Err: err}
}

func shortDiagnostic(diagnostic string, err error) string {
	diagnostic = strings.TrimSpace(diagnostic)
	if diagnostic == "" && err != nil {
		diagnostic = err.Error()
	}
	if len(diagnostic) > 500 {
		diagnostic = diagnostic[:500] + "…"
	}
	return diagnostic
}
