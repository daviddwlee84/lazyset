package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"lazyset/internal/config"
	"lazyset/internal/core"
)

func TestStartupIDsAndSetsAreResolvedWithoutDiscovery(t *testing.T) {
	path := configFixture(t, `[[sets]]
id = "mine"
tools = ["nvtop", "btop"]
`)
	f := commandFixture(t, true)
	args := []string{"--config", path, "--set", "all", "--tool", "top", "--start-set", "mine", "--start-set", "gpu", "--", "btop", "nvtop", "btop"}
	if code := Run(args, "dev", f.deps); code != 0 {
		t.Fatal(f.stderr.String())
	}
	if !f.tuiCalled || !reflect.DeepEqual(f.options.Tools, []string{"top", "btop", "nvtop", "nvitop"}) || len(f.options.StartSets) != 0 || f.options.Tool != "" {
		t.Fatalf("startup resolution lost order/deduplication: %+v", f.options)
	}
	if f.options.Set != "all" || f.manager.called || f.stdout.Len() != 0 || f.stderr.Len() != 0 {
		t.Fatal("startup resolution changed view, discovered tools, or wrote unexpected output")
	}
	if f.options.Sources.MainPath != path || f.options.Sources.HostsPath != filepath.Join(filepath.Dir(path), "hosts.toml") || !f.options.Sources.MainExplicit || f.options.Sources.HostsExplicit {
		t.Fatalf("lost source selection: %+v", f.options.Sources)
	}
}

func TestUnknownStartupRequestsWarnAndKeepValidTools(t *testing.T) {
	path := configFixture(t, "")
	f := commandFixture(t, true)
	if code := Run([]string{"--config", path, "--tool", "absent", "--start-set", "missing", "--", "top", "bad\n\x1b[2Jid"}, "dev", f.deps); code != 0 {
		t.Fatal(f.stderr.String())
	}
	if !f.tuiCalled || !reflect.DeepEqual(f.options.Tools, []string{"top"}) || len(f.options.Warnings) != 3 {
		t.Fatalf("valid startup request was lost: %+v", f.options)
	}
	if f.stdout.Len() != 0 || strings.Count(f.stderr.String(), "Warning:") != 3 || strings.ContainsAny(f.stderr.String(), "\x1b\r") {
		t.Fatalf("warnings did not stay sanitized on stderr: %q / %q", f.stdout.String(), f.stderr.String())
	}
	// A fully invalid startup list still opens the dashboard for exploration.
	f = commandFixture(t, true)
	if code := Run([]string{"--config", path, "--", "absent"}, "dev", f.deps); code != 0 || !f.tuiCalled || len(f.options.Tools) != 0 {
		t.Fatalf("unknown-only request blocked the dashboard: %d / %s", code, f.stderr.String())
	}
}

func TestToolIDsRequireDashSeparator(t *testing.T) {
	path := configFixture(t, "")
	for _, args := range [][]string{{"btop"}, {"btop", "--", "top"}, {"tools", "list", "--", "top"}} {
		f := commandFixture(t, true)
		if code := Run(append([]string{"--config", path}, args...), "dev", f.deps); code != 2 || f.tuiCalled || f.manager.called {
			t.Fatalf("ordinary command typo treated as a startup ID: %v / %d / %s", args, code, f.stderr.String())
		}
	}
	// After -- even command names and flag-looking strings are tool IDs, never
	// subcommands, shell code, or flags to the selected child executable.
	f := commandFixture(t, true)
	if code := Run([]string{"--config", path, "--", "tools", "--help", "$(false)", "top"}, "dev", f.deps); code != 0 || !f.tuiCalled || !reflect.DeepEqual(f.options.Tools, []string{"top"}) {
		t.Fatalf("data after -- changed command intent: %d / %s", code, f.stderr.String())
	}
}

func TestHostsPathInspectionDoesNotLoadOrCreateFiles(t *testing.T) {
	mainPath := filepath.Join(t.TempDir(), "not-created", "config.toml")
	override := filepath.Join(t.TempDir(), "private", "hosts.toml")
	for _, explicit := range []bool{false, true} {
		f := commandFixture(t, false)
		args := []string{"--config", mainPath}
		want := filepath.Join(filepath.Dir(mainPath), "hosts.toml")
		if explicit {
			args = append(args, "--hosts-config", override)
			want = override
		}
		args = append(args, "config", "path", "--hosts")
		if code := Run(args, "dev", f.deps); code != 0 || strings.TrimSpace(f.stdout.String()) != want || f.stderr.Len() != 0 {
			t.Fatalf("hosts path lookup failed: %d / %q / %s", code, f.stdout.String(), f.stderr.String())
		}
		if _, err := os.Stat(filepath.Dir(mainPath)); !os.IsNotExist(err) {
			t.Fatal("path inspection created configuration state")
		}
		if _, err := os.Stat(override); !os.IsNotExist(err) {
			t.Fatal("path inspection created hosts file")
		}
	}
}

func TestSeparateHostsConfigurationAndJSONRedaction(t *testing.T) {
	mainPath := configFixture(t, `[[tools]]
id = "btop"
env = {TOKEN = "private-tool"}
`)
	hostsPath := filepath.Join(t.TempDir(), "machine.toml")
	if err := os.WriteFile(hostsPath, []byte("default_host = 'server'\n[[hosts]]\nid = 'server'\nssh = 'server-alias'\nenv = { TOKEN = 'private-host' }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f := commandFixture(t, false)
	if code := Run([]string{"--config", mainPath, "--hosts-config", hostsPath, "config", "show", "--json"}, "dev", f.deps); code != 0 {
		t.Fatal(f.stderr.String())
	}
	var result struct {
		Path      string      `json:"path"`
		HostsPath string      `json:"hosts_path"`
		Config    core.Config `json:"config"`
	}
	if err := json.Unmarshal(f.stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Path != mainPath || result.HostsPath != hostsPath || result.Config.DefaultHost != "server" || strings.Contains(f.stdout.String(), "private-") || f.stderr.Len() != 0 {
		t.Fatalf("wrong merged/redacted config: %s / %s", f.stdout.String(), f.stderr.String())
	}
	f = commandFixture(t, true)
	if code := Run([]string{"--config", mainPath, "--hosts-config", hostsPath}, "dev", f.deps); code != 0 || !f.options.Sources.HostsExplicit || f.options.Sources.HostsPath != hostsPath {
		t.Fatalf("dashboard lost host source: %d / %+v", code, f.options.Sources)
	}
}

func TestLegacyHostsWarningDoesNotPolluteJSON(t *testing.T) {
	path := configFixture(t, "[[hosts]]\nid='server'\nssh='alias'\n")
	f := commandFixture(t, false)
	if code := Run([]string{"--config", path, "config", "show", "--json"}, "dev", f.deps); code != 0 || !json.Valid(f.stdout.Bytes()) || !strings.Contains(f.stderr.String(), "Warning:") {
		t.Fatalf("legacy warning missing or mixed into JSON: %d / %s / %s", code, f.stdout.String(), f.stderr.String())
	}
}

func TestHostsEditorCanRepairMalformedHostsWithoutRewritingMain(t *testing.T) {
	mainPath := configFixture(t, "# shared settings\nmouse=false\n")
	hostsPath := filepath.Join(filepath.Dir(mainPath), "hosts.toml")
	if err := os.WriteFile(hostsPath, []byte("bad TOML!"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", "/usr/bin/true")
	t.Setenv("EDITOR", "")
	f := commandFixture(t, true)
	f.deps.RunEditor = func(cmd *exec.Cmd) error {
		if cmd.Args[len(cmd.Args)-1] != hostsPath || cmd.Stdin != f.deps.In || cmd.Stdout != f.deps.Out || cmd.Stderr != f.deps.ErrOut {
			t.Fatal("hosts editor selected wrong file or terminal streams")
		}
		return os.WriteFile(hostsPath, []byte("[[hosts]]\nid='server'\nssh='alias'\n"), 0600)
	}
	if code := Run([]string{"--config", mainPath, "config", "edit", "--hosts"}, "dev", f.deps); code != 0 {
		t.Fatal(f.stderr.String())
	}
	data, err := os.ReadFile(mainPath)
	if err != nil || string(data) != "# shared settings\nmouse=false\n" {
		t.Fatal("editing hosts changed main configuration")
	}
	f = commandFixture(t, true)
	f.deps.RunEditor = func(*exec.Cmd) error { return os.WriteFile(hostsPath, []byte("still malformed!"), 0600) }
	if code := Run([]string{"--config", mainPath, "config", "edit", "--hosts"}, "dev", f.deps); code != 2 || !strings.Contains(f.stderr.String(), "config edit --hosts") {
		t.Fatalf("invalid hosts edit lacked repair guidance: %d / %s", code, f.stderr.String())
	}
	data, _ = os.ReadFile(hostsPath)
	if string(data) != "still malformed!" {
		t.Fatal("invalid hosts edit was discarded")
	}
}

func TestToolJSONIncludesEffectiveReturnPolicy(t *testing.T) {
	path := configFixture(t, `[[tools]]
id = "top"
return_keys = ["q", "esc"]
`)
	f := commandFixture(t, false)
	if code := Run([]string{"--config", path, "tools", "list", "--installed", "--json"}, "dev", f.deps); code != 0 {
		t.Fatal(f.stderr.String())
	}
	var result toolsOutput
	if err := json.Unmarshal(f.stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	base, _ := config.Defaults().Tool("top")
	if len(result.Tools) != 1 || !reflect.DeepEqual(result.Tools[0].ReturnKeys, []string{"q", "esc"}) || result.Tools[0].QuitHint != base.QuitHint || !reflect.DeepEqual(result.Tools[0].QuitKeys, base.QuitKeys) || result.Tools[0].QuitSource != base.QuitSource {
		t.Fatalf("return policy missing from machine output: %+v", result)
	}
}
