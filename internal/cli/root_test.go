package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazyset/internal/catalog"
	"github.com/daviddwlee84/lazyset/internal/config"
	"github.com/daviddwlee84/lazyset/internal/core"
	"github.com/daviddwlee84/lazyset/internal/host"
	"github.com/daviddwlee84/lazyset/internal/tui"
)

type fakeDiscovery struct {
	err                error
	unknown            bool
	called, closed     bool
	host               string
	topPath, topReason string
}

func (f *fakeDiscovery) Discover(_ context.Context, h core.Host, tools []core.Tool) ([]core.Availability, error) {
	f.called = true
	f.host = h.ID
	var rows []core.Availability
	for _, tool := range tools {
		row := core.Availability{ToolID: tool.ID, State: "missing", Reason: "not on PATH"}
		if tool.ID == "top" {
			row.State = "found"
			row.Reason = ""
			row.Path = "/usr/bin/top"
			if f.topPath != "" {
				row.Path = f.topPath
			}
			if f.topReason != "" {
				row.Reason = f.topReason
			}
		}
		if f.unknown {
			row.State = "unknown"
			row.Reason = "authentication required"
			row.Path = ""
		}
		rows = append(rows, row)
	}
	return rows, f.err
}
func (f *fakeDiscovery) Close() error { f.closed = true; return nil }

type testCommand struct {
	stdout, stderr bytes.Buffer
	deps           Dependencies
	manager        *fakeDiscovery
	tuiCalled      bool
	options        tui.Options
}

func commandFixture(t *testing.T, tty bool) *testCommand {
	t.Helper()
	f := &testCommand{manager: &fakeDiscovery{}}
	f.deps = Dependencies{
		In: strings.NewReader(""), Out: &f.stdout, ErrOut: &f.stderr,
		IsTerminal: func(io.Reader, io.Writer) bool { return tty },
		NewManager: func() DiscoveryManager { return f.manager },
		RunTUI: func(_ core.Config, _ string, options tui.Options) error {
			f.tuiCalled = true
			f.options = options
			return nil
		},
		RunEditor: func(*exec.Cmd) error { t.Fatal("unexpected editor launch"); return nil },
	}
	return f
}

func configFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHelpVersionAndPathIgnoreMalformedConfiguration(t *testing.T) {
	path := configFixture(t, "not valid TOML !")
	for _, args := range [][]string{{"--help"}, {"tools", "--help"}, {"config", "--help"}, {"version"}, {"--version"}, {"config", "path"}, {}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f := commandFixture(t, false)
			code := Run(append([]string{"--config", path}, args...), "test-version", f.deps)
			if code != 0 || f.stderr.Len() != 0 {
				t.Fatalf("code=%d stderr=%s", code, f.stderr.String())
			}
			if f.stdout.Len() == 0 || f.manager.called || f.tuiCalled {
				t.Fatal("read-only command initialized runtime or gave no output")
			}
		})
	}
}

func TestNonTTYIntentAndInvalidArgumentsNeverCreateConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "config.toml")
	for _, args := range [][]string{
		{"--host", "local"}, {"--set", "system"}, {"--tool", "top"}, {"--start-set", "gpu"}, {"--", "top"},
		{"config", "edit"}, {"config", "edit", "--hosts"}, {"config", "edit", "--json"},
		{"tools", "list", "--insatlled"}, {"config", "path", "extra"}, {"does-not-exist"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f := commandFixture(t, false)
			if code := Run(append([]string{"--config", path}, args...), "dev", f.deps); code != 2 {
				t.Fatalf("got code %d, stderr %s", code, f.stderr.String())
			}
			if f.manager.called || f.tuiCalled {
				t.Fatal("invalid invocation initialized runtime")
			}
			if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
				t.Fatal("invocation created configuration state")
			}
		})
	}
}

func TestExplicitDashboardSelectionsAreValidatedBeforeLaunch(t *testing.T) {
	path := configFixture(t, "[[hosts]]\nid='server'\nssh='server-alias'\n")
	f := commandFixture(t, true)
	if code := Run([]string{"--config", path, "--host", "server", "--set", "gpu", "--tool", "top"}, "dev", f.deps); code != 0 {
		t.Fatal(f.stderr.String())
	}
	if !f.tuiCalled || f.options.Host != "server" || f.options.Set != "gpu" || !reflect.DeepEqual(f.options.Tools, []string{"top"}) || f.options.Tool != "" {
		t.Fatalf("lost selected options: %+v", f.options)
	}
	for _, args := range [][]string{{"--host", "missing"}, {"--set", "missing"}, {"--host", ""}, {"--tool", ""}, {"--start-set", ""}, {"--start-set", "system,,gpu"}} {
		f := commandFixture(t, true)
		if code := Run(append([]string{"--config", path}, args...), "dev", f.deps); code != 2 || f.tuiCalled {
			t.Fatalf("invalid selection launched: code %d; %s", code, f.stderr.String())
		}
	}
}

func TestDashboardAcceptsAllSet(t *testing.T) {
	path := configFixture(t, "")
	f := commandFixture(t, true)
	if code := Run([]string{"--config", path, "--set", "all"}, "dev", f.deps); code != 0 {
		t.Fatalf("--set all failed: %s", f.stderr.String())
	}
	if !f.tuiCalled || f.options.Set != catalog.AllSetID || f.manager.called {
		t.Fatalf("All selection did not reach the dashboard cleanly: %+v", f.options)
	}
}

func TestToolsJSONAndInstalledFilter(t *testing.T) {
	path := configFixture(t, "")
	f := commandFixture(t, false)
	if code := Run([]string{"--config", path, "tools", "list", "--installed", "--json"}, "dev", f.deps); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, f.stderr.String())
	}
	var result toolsOutput
	if err := json.Unmarshal(f.stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not JSON: %s; %v", f.stdout.String(), err)
	}
	if result.Host != "local" || len(result.Tools) != 1 || result.Tools[0].ID != "top" || result.Tools[0].State != "found" {
		t.Fatalf("wrong output: %+v", result)
	}
	if f.stderr.Len() != 0 || !f.manager.called || !f.manager.closed {
		t.Fatal("unexpected diagnostics or manager lifecycle")
	}
}

func TestUnknownDiscoveryProducesJSONAndSeparateDiagnostic(t *testing.T) {
	path := configFixture(t, "[[hosts]]\nid='server'\nssh='server-alias'\n")
	f := commandFixture(t, false)
	f.manager.unknown = true
	f.manager.err = &host.Error{Kind: host.Authentication, HostID: "server", Detail: "authenticate with the dashboard Connect action"}
	if code := Run([]string{"--config", path, "tools", "list", "--host", "server", "--json"}, "dev", f.deps); code != 1 {
		t.Fatalf("code %d", code)
	}
	var result toolsOutput
	if err := json.Unmarshal(f.stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Error == nil || result.Error.Kind != host.Authentication || len(result.Tools) != len(config.Defaults().Tools) {
		t.Fatalf("missing structured error: %+v", result)
	}
	for _, row := range result.Tools {
		if row.State != "unknown" {
			t.Fatalf("unknown became absence: %+v", row)
		}
	}
	if !strings.Contains(f.stderr.String(), "authenticate") || f.manager.host != "server" {
		t.Fatal("missing separate diagnostic or wrong host")
	}
}

func TestPartialUnknownWithoutServiceErrorIsNotSuccess(t *testing.T) {
	f := commandFixture(t, false)
	f.manager.unknown = true
	if code := Run([]string{"--config", configFixture(t, ""), "tools", "list", "--installed", "--json"}, "dev", f.deps); code != 1 {
		t.Fatalf("got code %d", code)
	}
	var result toolsOutput
	if err := json.Unmarshal(f.stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Tools) != 0 || result.Error == nil {
		t.Fatalf("filtered unknown must retain error: %+v", result)
	}
}

func TestSetsListJSONDoesNotDiscover(t *testing.T) {
	f := commandFixture(t, false)
	if code := Run([]string{"--config", configFixture(t, ""), "sets", "list", "--json"}, "dev", f.deps); code != 0 {
		t.Fatal(f.stderr.String())
	}
	var sets []core.Set
	if err := json.Unmarshal(f.stdout.Bytes(), &sets); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sets, config.Defaults().Sets) || f.manager.called || f.tuiCalled || f.stderr.Len() != 0 {
		t.Fatal("wrong sets or unintended discovery")
	}
}

func TestSetsListAllIncludesCustomToolsWithoutDiscovery(t *testing.T) {
	path := configFixture(t, `default_set = 'all'
[[tools]]
id = 'my-dashboard'
command = ['my-dashboard']
[[sets]]
id = 'mine'
tools = ['my-dashboard', 'btop']
`)
	f := commandFixture(t, false)
	if code := Run([]string{"--config", path, "sets", "list", "--json"}, "dev", f.deps); code != 0 {
		t.Fatal(f.stderr.String())
	}
	var sets []core.Set
	if err := json.Unmarshal(f.stdout.Bytes(), &sets); err != nil {
		t.Fatal(err)
	}
	cfg := core.Config{Sets: sets}
	all, ok := cfg.Set(catalog.AllSetID)
	want := append(catalog.AllSet(catalog.Tools()).Tools, "my-dashboard")
	if !ok || !all.Builtin || !reflect.DeepEqual(all.Tools, want) {
		t.Fatalf("scriptable All membership omitted or reordered custom tools: %+v", all)
	}
	if mine, ok := cfg.Set("mine"); !ok || mine.Builtin || !reflect.DeepEqual(mine.Tools, []string{"my-dashboard", "btop"}) {
		t.Fatalf("scriptable custom set changed: %+v", mine)
	}
	if f.manager.called || f.tuiCalled || f.stderr.Len() != 0 {
		t.Fatal("set inspection initialized runtime or mixed diagnostics into output")
	}
}

func TestConfigShowRedactsEveryEnvironmentValue(t *testing.T) {
	path := configFixture(t, `[[hosts]]
id='local'
env={TOKEN='private-host-value',LANG='private-plain-value'}
[[tools]]
id='btop'
env={TOKEN='private-tool-value'}
`)
	for _, jsonMode := range []bool{false, true} {
		f := commandFixture(t, false)
		args := []string{"--config", path, "config", "show"}
		if jsonMode {
			args = append(args, "--json")
		}
		if code := Run(args, "dev", f.deps); code != 0 {
			t.Fatal(f.stderr.String())
		}
		if strings.Contains(f.stdout.String(), "private-") || !strings.Contains(f.stdout.String(), "<redacted>") {
			t.Fatal("environment values were exposed")
		}
		if jsonMode {
			var out any
			if err := json.Unmarshal(f.stdout.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestEditorCanRepairMalformedExplicitConfig(t *testing.T) {
	dir := t.TempDir()
	editor := filepath.Join(dir, "editor")
	if err := os.WriteFile(editor, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", editor)
	t.Setenv("EDITOR", "")
	path := configFixture(t, "bad TOML!")
	f := commandFixture(t, true)
	f.deps.RunEditor = func(cmd *exec.Cmd) error {
		if cmd.Stdin != f.deps.In || cmd.Stdout != f.deps.Out || cmd.Stderr != f.deps.ErrOut {
			t.Fatal("editor did not receive native streams")
		}
		if got := cmd.Args[len(cmd.Args)-1]; got != path {
			t.Fatalf("editor path %q", got)
		}
		return os.WriteFile(path, []byte("mouse=false\n"), 0600)
	}
	if code := Run([]string{"--config", path, "config", "edit"}, "dev", f.deps); code != 0 {
		t.Fatal(f.stderr.String())
	}
	if !strings.Contains(f.stdout.String(), "valid") {
		t.Fatal("missing validation result")
	}
	f = commandFixture(t, true)
	f.deps.RunEditor = func(*exec.Cmd) error { return os.WriteFile(path, []byte("still malformed!"), 0600) }
	if code := Run([]string{"--config", path, "config", "edit"}, "dev", f.deps); code != 2 {
		t.Fatalf("code %d", code)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "still malformed!" {
		t.Fatal("invalid user edits were discarded")
	}
}

func TestTerminalDetectionRejectsCharacterDeviceAndBuffers(t *testing.T) {
	device, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer device.Close()
	if isTerminalPair(device, device) || isTerminalPair(strings.NewReader(""), io.Discard) {
		t.Fatal("nonterminal accepted")
	}
}

func TestCancellationExitCode(t *testing.T) {
	if ExitCode(context.Canceled) != 130 || ExitCode(errors.New("runtime failure")) != 1 || ExitCode(usageError(errors.New("bad option"))) != 2 {
		t.Fatal("wrong exit status")
	}
}

func TestHumanOutputCannotInjectRowsOrTerminalControls(t *testing.T) {
	path := configFixture(t, `[[tools]]
id = 'top'
description = "purpose\nFORGED_DESCRIPTION\u001b[31mred\u001b[0m"
`)
	discoveredPath := "/tools/top\nFORGED_PATH\x1b[2J"
	reason := "reason\r\nFORGED_REASON\x1b]0;changed-title\a\tend"
	diagnostic := "remote failure\nFORGED_DIAGNOSTIC\x1b[2J\r\tend"
	f := commandFixture(t, false)
	f.manager.topPath, f.manager.topReason = discoveredPath, reason
	f.manager.err = errors.New(diagnostic)
	if code := Run([]string{"--config", path, "tools", "list", "--installed"}, "dev", f.deps); code != 1 {
		t.Fatalf("exit code %d", code)
	}
	if strings.Count(f.stdout.String(), "\n") != 2 {
		t.Fatalf("external text injected table rows: %q", f.stdout.String())
	}
	if strings.Count(f.stderr.String(), "\n") != 1 {
		t.Fatalf("external text injected diagnostics: %q", f.stderr.String())
	}
	for _, value := range []string{f.stdout.String(), f.stderr.String()} {
		if strings.ContainsAny(value, "\x1b\r\a\t") || strings.Contains(value, "changed-title") {
			t.Fatalf("terminal control leaked: %q", value)
		}
	}
	// The machine interface preserves the original data, safely JSON-escaped.
	f = commandFixture(t, false)
	f.manager.topPath, f.manager.topReason = discoveredPath, reason
	f.manager.err = errors.New(diagnostic)
	if code := Run([]string{"--config", path, "tools", "list", "--installed", "--json"}, "dev", f.deps); code != 1 {
		t.Fatalf("JSON exit code %d", code)
	}
	var result toolsOutput
	if err := json.Unmarshal(f.stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Tools) != 1 || result.Tools[0].Path != discoveredPath || result.Tools[0].Reason != reason || result.Tools[0].Description != "purpose\nFORGED_DESCRIPTION\x1b[31mred\x1b[0m" {
		t.Fatalf("machine data was altered: %+v", result)
	}
	if result.Error == nil || result.Error.Message != diagnostic {
		t.Fatal("JSON diagnostic lost its original value")
	}
	if strings.ContainsRune(f.stdout.String(), '\x1b') {
		t.Fatal("JSON contains an unescaped ESC")
	}
}
