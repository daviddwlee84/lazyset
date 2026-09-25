package host

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/lazyset/internal/core"
)

func writeExecutable(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLocalDiscoveryUsesToolPathWithoutExecuting(t *testing.T) {
	hostDir, toolDir := t.TempDir(), t.TempDir()
	marker := filepath.Join(toolDir, "executed")
	writeExecutable(t, hostDir, "monitor", "exit 99")
	toolPath := writeExecutable(t, toolDir, "monitor", "touch "+shellQuote(marker))
	tools := []core.Tool{
		{ID: "monitor", Command: []string{"monitor"}, Env: map[string]string{"PATH": toolDir}},
		{ID: "absent", Command: []string{"absent"}},
		{ID: "other-os", Command: []string{"monitor"}, Platforms: []string{"nonexistent-platform"}},
	}
	rows, err := NewManager().Discover(context.Background(), core.Host{ID: "local", Env: map[string]string{"PATH": hostDir}}, tools)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].State != "found" || rows[0].Path != toolPath {
		t.Fatalf("found: %+v", rows[0])
	}
	if rows[1].State != "missing" || rows[2].State != "unsupported" {
		t.Fatalf("states: %+v", rows)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ordinary tool was executed during discovery")
	}
}

func TestLocalRelativePathUsesToolDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	want := writeExecutable(t, filepath.Join(dir, "bin"), "monitor", "exit 0")
	tool := core.Tool{ID: "monitor", Command: []string{"monitor", "--foo"}, Dir: dir, Env: map[string]string{"PATH": "bin"}}
	m := NewManager()
	rows, err := m.Discover(context.Background(), core.Host{ID: "local"}, []core.Tool{tool})
	if err != nil || rows[0].Path != want {
		t.Fatalf("%+v %v", rows, err)
	}
	spec, err := m.LaunchSpec(core.Host{ID: "local", Env: map[string]string{"FOO": "host"}}, tool, rows[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Command[0] != want || spec.Command[1] != "--foo" || spec.Dir != dir || spec.Env["FOO"] != "host" {
		t.Fatalf("%+v", spec)
	}
}

func TestGDUCollisionAndVersionIdentity(t *testing.T) {
	dir := t.TempDir()
	coreDir := filepath.Join(dir, "coreutils", "bin")
	if err := os.MkdirAll(coreDir, 0755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "ran-coreutils")
	real := writeExecutable(t, coreDir, "gdu", "touch "+shellQuote(marker))
	if err := os.Symlink(real, filepath.Join(dir, "gdu")); err != nil {
		t.Fatal(err)
	}
	tool := core.Tool{ID: "gdu", Command: []string{"gdu"}, Candidates: []string{"gdu-go", "gdu"}}
	h := core.Host{ID: "local", Env: map[string]string{"PATH": dir}}
	rows, err := NewManager().Discover(context.Background(), h, []core.Tool{tool})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].State != "missing" || !strings.Contains(rows[0].Reason, "coreutils") {
		t.Fatalf("%+v", rows)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("symlink identity should avoid running coreutils")
	}
	if err := os.Remove(filepath.Join(dir, "gdu")); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, dir, "gdu", "printf 'Version: 5.0\\nBuilt time: today\\nBuilt user: ci\\n'")
	rows, err = NewManager().Discover(context.Background(), h, []core.Tool{tool})
	if err != nil || rows[0].State != "found" {
		t.Fatalf("%+v %v", rows, err)
	}
	preferred := writeExecutable(t, dir, "gdu-go", "exit 99")
	rows, err = NewManager().Discover(context.Background(), h, []core.Tool{tool})
	if err != nil || rows[0].Path != preferred {
		t.Fatalf("%+v %v", rows, err)
	}
}

func fakeSSH(t *testing.T, shared bool) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "ssh.log")
	t.Setenv("FAKE_SSH_LOG", log)
	config := "hostname fake.invalid\ncontrolmaster false\ncontrolpersist no\n"
	if shared {
		config += "controlpath /user/owned/socket\ncontrolmaster auto\ncontrolpersist 3600\n"
	}
	t.Setenv("FAKE_SSH_CONFIG", config)
	t.Setenv("FAKE_SSH_FAIL", "")
	t.Setenv("FAKE_SSH_MODE", "")
	script := `printf '%s\000' "$@" >> "$FAKE_SSH_LOG"
printf '\n' >> "$FAKE_SSH_LOG"
for arg do
  if [ "$arg" = -G ]; then printf '%s' "$FAKE_SSH_CONFIG"; exit 0; fi
  if [ "$arg" = -O ]; then exit 0; fi
done
if [ -n "$FAKE_SSH_FAIL" ]; then printf '%s\n' "$FAKE_SSH_FAIL" >&2; exit 255; fi
if [ "$FAKE_SSH_MODE" = sleep ]; then exec /bin/sleep 10; fi
for arg do
  if [ "$arg" = -N ]; then exit 0; fi
  last=$arg
done
printf 'Welcome! This banner has tabs, newlines and bogus missing entries.\n'
/bin/sh -c "$last"
status=$?
printf '\nGoodbye banner\n'
exit "$status"`
	m := NewManager()
	m.sshBinary = writeExecutable(t, dir, "ssh", script)
	t.Cleanup(func() { _ = m.Close() })
	return m, log
}

func TestRemoteDiscoveryFramingEnvironmentAndNoToolExecution(t *testing.T) {
	m, log := fakeSSH(t, false)
	dir := t.TempDir()
	marker := filepath.Join(dir, "executed")
	path := writeExecutable(t, dir, "monitor", "touch "+shellQuote(marker))
	h := core.Host{ID: "remote", SSH: "test-alias", Env: map[string]string{"PATH": "/unavailable"}}
	tools := []core.Tool{
		{ID: "monitor", Command: []string{"monitor"}, Env: map[string]string{"PATH": dir}},
		{ID: "absent", Command: []string{"does-not-exist"}},
		{ID: "unsupported", Command: []string{"monitor"}, Env: map[string]string{"PATH": dir}, Platforms: []string{"other-os"}},
	}
	rows, err := m.Discover(context.Background(), h, tools)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].State != "found" || rows[0].Path != path || rows[1].State != "missing" || rows[2].State != "unsupported" {
		t.Fatalf("%+v", rows)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("discovery executed tool")
	}
	args, _ := os.ReadFile(log)
	if !bytes.Contains(args, []byte("BatchMode=yes")) || !bytes.Contains(args, []byte("StrictHostKeyChecking=yes")) || !bytes.Contains(args, []byte("test-alias")) {
		t.Fatalf("missing SSH options: %q", args)
	}
	if !bytes.Contains(args, []byte("ControlPath=")) {
		t.Fatal("private fallback not supplied")
	}
}

func TestRemoteLaunchPreservesArgvAndLiteralEnvironment(t *testing.T) {
	m, _ := fakeSSH(t, true)
	dir := filepath.Join(t.TempDir(), "a'b dir")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "must-not-exist")
	value := "value' $(touch " + marker + ")\nsecond line"
	path := writeExecutable(t, dir, "monitor", `printf '%s\000' "$PWD" "$SPECIAL" "$@"`)
	h := core.Host{ID: "remote", SSH: "test-alias", Env: map[string]string{"SPECIAL": "host"}}
	tool := core.Tool{ID: "monitor", Command: []string{"monitor", "literal' argument", "$(touch " + marker + ")", "line1\nline2"}, Dir: dir, Env: map[string]string{"SPECIAL": value}}
	spec, err := m.LaunchSpec(h, tool, path)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Command[0] != m.sshBinary {
		t.Fatalf("%+v", spec)
	}
	joined := strings.Join(spec.Command, "\x00")
	if !strings.Contains(joined, "-tt") || !strings.Contains(joined, "EscapeChar=none") || strings.Contains(joined, "ControlPath=") {
		t.Fatalf("%q", spec.Command)
	}
	remote := spec.Command[len(spec.Command)-1]
	output, err := exec.Command("/bin/sh", "-c", remote).Output()
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join(append([]string{dir, value}, tool.Command[1:]...), "\x00") + "\x00"
	if string(output) != want {
		t.Fatalf("got %q want %q", output, want)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("shell expanded literal input")
	}
}

func TestRemoteGDUIdentityWithRestrictedPath(t *testing.T) {
	m, _ := fakeSSH(t, false)
	dir := t.TempDir()
	h := core.Host{ID: "r", SSH: "alias", Env: map[string]string{"PATH": dir}}
	tool := core.Tool{ID: "gdu", Command: []string{"gdu"}, Candidates: []string{"gdu-go", "gdu"}}
	writeExecutable(t, dir, "gdu", "printf 'du (GNU coreutils) 9.0\\n'")
	rows, err := m.Discover(context.Background(), h, []core.Tool{tool})
	if err != nil || rows[0].State != "missing" || !strings.Contains(rows[0].Reason, "coreutils") {
		t.Fatalf("%+v %v", rows, err)
	}
	writeExecutable(t, dir, "gdu", "printf 'Version: 5.0\\nBuilt time: today\\nBuilt user: ci\\n'")
	rows, err = m.Discover(context.Background(), h, []core.Tool{tool})
	if err != nil || rows[0].State != "found" {
		t.Fatalf("%+v %v", rows, err)
	}
	writeExecutable(t, dir, "gdu", "exec /bin/sleep 10")
	start := time.Now()
	rows, err = m.Discover(context.Background(), h, []core.Tool{tool})
	if err != nil || rows[0].State != "unknown" {
		t.Fatalf("%+v %v", rows, err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("gdu version probe did not honor its bound")
	}
}

func TestRemoteFailuresStayUnknown(t *testing.T) {
	for _, tc := range []struct{ message, kind string }{
		{"Permission denied (publickey,password).", Authentication},
		{"Host key verification failed.", Authentication},
		{"ssh: connect to host test: Connection refused", Offline},
		{"some unexpected remote failure", Discovery},
	} {
		t.Run(tc.kind+tc.message, func(t *testing.T) {
			m, _ := fakeSSH(t, false)
			t.Setenv("FAKE_SSH_FAIL", tc.message)
			rows, err := m.Discover(context.Background(), core.Host{ID: "r", SSH: "alias"}, []core.Tool{{ID: "btop", Command: []string{"btop"}}})
			var typed *Error
			if !errors.As(err, &typed) || typed.Kind != tc.kind || rows[0].State != "unknown" {
				t.Fatalf("%+v %v", rows, err)
			}
		})
	}
}

func TestRemoteTimeoutBounded(t *testing.T) {
	m, _ := fakeSSH(t, false)
	if _, err := m.policy(context.Background(), core.Host{ID: "r", SSH: "alias"}); err != nil {
		t.Fatal(err)
	}
	m.probeTimeout = 80 * time.Millisecond
	t.Setenv("FAKE_SSH_MODE", "sleep")
	start := time.Now()
	rows, err := m.Discover(context.Background(), core.Host{ID: "r", SSH: "alias"}, []core.Tool{{ID: "btop", Command: []string{"btop"}}})
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != Offline || rows[0].State != "unknown" {
		t.Fatalf("%+v %v", rows, err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("probe did not honor timeout")
	}
}

func TestPrivateConnectAndCloseOwnership(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{true: "shared", false: "owned"}[shared], func(t *testing.T) {
			m, log := fakeSSH(t, shared)
			h := core.Host{ID: "r", SSH: "alias"}
			cmd, err := m.ConnectCommand(context.Background(), h)
			if err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(cmd.Args, " ")
			if !strings.Contains(joined, "BatchMode=no") {
				t.Fatal(joined)
			}
			if !shared && (!strings.Contains(joined, "-N -f") || !strings.Contains(joined, "ControlPath=")) {
				t.Fatal(joined)
			}
			if shared && strings.Contains(joined, "ControlPath=") {
				t.Fatal("overrode shared configuration")
			}
			if err := cmd.Run(); err != nil {
				t.Fatal(err)
			}
			p := m.policies[h.SSH]
			if !shared {
				info, err := os.Stat(m.controlDir)
				if err != nil || info.Mode().Perm() != 0700 {
					t.Fatalf("private directory permissions: %v %v", info, err)
				}
				if err := os.WriteFile(p.path, []byte{}, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}
			content, _ := os.ReadFile(log)
			hasExit := bytes.Contains(content, []byte("-O\x00exit"))
			if hasExit == shared {
				t.Fatalf("ownership cleanup incorrect: %q", content)
			}
			if !shared {
				if _, err := os.Stat(filepath.Dir(p.path)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("owned directory remains")
				}
			}
		})
	}
}

func TestParserRejectsIncompleteOrWrongFrames(t *testing.T) {
	tools := []core.Tool{{ID: "x", Command: []string{"x"}, Platforms: []string{runtime.GOOS}}}
	for _, data := range []string{"banner only", "\x00token:begin\x00Linux\x000\x00missing\x00", "\x00token:begin\x00Linux\x001\x00found\x00/bin/x\x00\x00\x00token:end\x00"} {
		if _, err := parseDiscovery([]byte(data), "token", tools); err == nil {
			t.Fatalf("accepted malformed frame %q", data)
		}
	}
}

func TestInvalidEnvironmentAndDestinationRejected(t *testing.T) {
	m := NewManager()
	if _, err := m.LaunchSpec(core.Host{}, core.Tool{Command: []string{"x"}, Env: map[string]string{"bad;name": "x"}}, ""); err == nil {
		t.Fatal("accepted invalid environment key")
	}
	if _, err := m.LaunchSpec(core.Host{ID: "bad", SSH: "-oProxyCommand=anything"}, core.Tool{Command: []string{"x"}}, ""); err == nil {
		t.Fatal("accepted option as destination")
	}
}
