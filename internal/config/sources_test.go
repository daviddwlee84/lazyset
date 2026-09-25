package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazyset/internal/core"
)

func writeHosts(t *testing.T, sources Sources, text string) {
	t.Helper()
	if err := os.WriteFile(sources.HostsPath, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func testSources(t *testing.T, main string) Sources {
	t.Helper()
	s, err := ResolveSources(writeConfig(t, main), "", true, false)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSourcesMergeBeforeValidationAndHostReplacement(t *testing.T) {
	s := testSources(t, `default_host='new'
[[hosts]]
id='server'
name='Legacy name'
ssh='legacy'
env={SECRET='legacy-only'}
[[tools]]
id='custom'
command=['custom']
`)
	writeHosts(t, s, `default_host='server'
[[hosts]]
id='server'
ssh='replacement'
[[hosts]]
id='new'
ssh='new-alias'
`)
	cfg, err := LoadSources(s)
	if err != nil {
		t.Fatal(err)
	}
	host, _ := cfg.Host("server")
	if cfg.DefaultHost != "server" || host.SSH != "replacement" || host.Name != "server" || len(host.Env) != 0 {
		t.Fatalf("host replacement retained legacy fields: %+v", host)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "config edit --hosts") {
		t.Fatalf("missing legacy migration guidance: %v", cfg.Warnings)
	}
	if _, ok := cfg.Tool("custom"); !ok {
		t.Fatal("host merge lost portable tools")
	}
	writeHosts(t, s, "[[hosts]]\nid='new'\nssh='new-alias'\n")
	cfg, err = LoadSources(s)
	if err != nil || cfg.DefaultHost != "new" {
		t.Fatalf("legacy default pointing into hosts file was validated too early: %v", err)
	}
}

func TestSourcesPathsMissingAndSymlinkSemantics(t *testing.T) {
	dir := t.TempDir()
	s, err := ResolveSources(filepath.Join(dir, "missing", "config.toml"), "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadSources(s)
	if err != nil || len(cfg.Hosts) != 1 || cfg.DefaultHost != "local" {
		t.Fatalf("missing optional sources: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(s.MainPath)); !os.IsNotExist(err) {
		t.Fatal("load created directories")
	}
	s.HostsExplicit = true
	if _, err := LoadSources(s); err == nil || !strings.Contains(err.Error(), s.HostsPath) {
		t.Fatalf("explicit missing hosts accepted: %v", err)
	}
	portable := writeConfig(t, "")
	link := filepath.Join(dir, "linked.toml")
	if err := os.Symlink(portable, link); err != nil {
		t.Fatal(err)
	}
	s, err = ResolveSources(link, "", true, false)
	if err != nil || s.HostsPath != filepath.Join(dir, "hosts.toml") {
		t.Fatalf("hosts followed config symlink: %+v %v", s, err)
	}
	if _, err := ResolveSources(link, link, true, true); err == nil {
		t.Fatal("accepted same destination for both schemas")
	}
}

func TestHostsSchemaAndRepairDestination(t *testing.T) {
	s := testSources(t, "")
	for _, content := range []string{"not TOML !", "mouse=false", "[[tools]]\nid='btop'"} {
		writeHosts(t, s, content)
		if _, err := LoadSources(s); err == nil || !strings.Contains(err.Error(), s.HostsPath) || !strings.Contains(err.Error(), "config edit --hosts") {
			t.Fatalf("hosts repair guidance: %v", err)
		}
	}
	writeHosts(t, s, "[[hosts]]\nid='server'\nssh='one'\n[[hosts]]\nid='server'\nssh='two'\n")
	if _, err := LoadSources(s); err == nil || !strings.Contains(err.Error(), "duplicate host") {
		t.Fatalf("duplicate hosts accepted: %v", err)
	}
}

func TestHostsCanExistWithoutPortablePreferences(t *testing.T) {
	s, err := ResolveSources(filepath.Join(t.TempDir(), "config.toml"), "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveHostSourcesIfUnchanged(s, core.Host{ID: "server", SSH: "alias"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.MainPath); !os.IsNotExist(err) {
		t.Fatal("saving a host created portable settings")
	}
	writeHosts(t, s, "default_host='server'\n[[hosts]]\nid='server'\nssh='alias'\n")
	cfg, err := LoadSources(s)
	if err != nil || cfg.DefaultHost != "server" || cfg.DefaultSet != "system" {
		t.Fatalf("hosts-only source: %v", err)
	}
}

func TestSourceWritesTargetOnlyTheirOwnFile(t *testing.T) {
	s := testSources(t, "# Portable preferences\nmouse=false\n")
	writeHosts(t, s, "# Private hosts\ndefault_host='server'\n[[hosts]]\nid='server'\nssh='server-alias' # keep\n")
	main, _ := Snapshot(s.MainPath)
	hosts, _ := Snapshot(s.HostsPath)
	if err := SaveHostSourcesIfUnchanged(s, core.Host{ID: "new", SSH: "new-alias"}, hosts); err != nil {
		t.Fatal(err)
	}
	gotMain, _ := Snapshot(s.MainPath)
	gotHosts, _ := Snapshot(s.HostsPath)
	if !bytes.Equal(main, gotMain) || !bytes.HasPrefix(gotHosts, hosts) {
		t.Fatal("host save changed portable settings or host comments")
	}
	if err := SaveSetSourcesIfUnchanged(s, core.Set{ID: "work", Tools: []string{"top"}}, main); err != nil {
		t.Fatal(err)
	}
	afterHosts, _ := Snapshot(s.HostsPath)
	if !bytes.Equal(gotHosts, afterHosts) {
		t.Fatal("set save changed hosts")
	}
	updated, _ := Snapshot(s.MainPath)
	if err := DeleteSetSourcesIfUnchanged(s, "work", updated); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadSources(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Host("new"); !ok {
		t.Fatal("new host not effective")
	}
	if _, ok := cfg.Set("work"); ok {
		t.Fatal("set remained after deletion")
	}
}

func TestHostSourceWritesRejectLegacyDuplicatesAndStaleDestination(t *testing.T) {
	s := testSources(t, "[[hosts]]\nid='legacy'\nssh='legacy-alias'\n")
	if err := SaveHostSourcesIfUnchanged(s, core.Host{ID: "legacy", SSH: "replacement"}, nil); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("accepted legacy duplicate: %v", err)
	}
	if _, err := os.Stat(s.HostsPath); !os.IsNotExist(err) {
		t.Fatal("rejected write created hosts")
	}
	writeHosts(t, s, "")
	if err := SaveHostSourcesIfUnchanged(s, core.Host{ID: "new", SSH: "new"}, nil); !errors.Is(err, ErrChanged) {
		t.Fatalf("stale missing snapshot: %v", err)
	}
}

func TestMergedWriterDetectsCompanionChanges(t *testing.T) {
	s := testSources(t, "# original")
	validate, err := sourceWriteValidator(s, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.MainPath, []byte("# changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validate(nil); !errors.Is(err, ErrChanged) {
		t.Fatalf("companion change not detected: %v", err)
	}
}

func TestHostsTemplateAndMalformedRepair(t *testing.T) {
	s := testSources(t, "")
	t.Setenv("VISUAL", "/usr/bin/true")
	cmd, err := EditHostsCommand(s.HostsPath)
	if err != nil || cmd.Args[len(cmd.Args)-1] != s.HostsPath {
		t.Fatalf("hosts editor: %v", err)
	}
	cfg, err := LoadSources(s)
	if err != nil || cfg.DefaultHost != "local" || len(cfg.Warnings) != 0 {
		t.Fatalf("hosts template invalid: %v", err)
	}
	data, _ := Snapshot(s.HostsPath)
	if bytes.Contains(data, []byte("mouse =")) || !bytes.Contains(data, []byte("default_host")) {
		t.Fatal("wrong hosts template")
	}
	writeHosts(t, s, "broken = [")
	if _, err := EditHostsCommand(s.HostsPath); err != nil {
		t.Fatal(err)
	}
	data, _ = Snapshot(s.HostsPath)
	if string(data) != "broken = [" {
		t.Fatal("editor replaced malformed host content")
	}
}

func TestHostWritePreservesSymlinkAndRejectsAliasedSources(t *testing.T) {
	s := testSources(t, "# portable\n")
	target := filepath.Join(t.TempDir(), "machine.toml")
	if err := os.WriteFile(target, []byte("# private hosts\n"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, s.HostsPath); err != nil {
		t.Fatal(err)
	}
	before, _ := Snapshot(s.HostsPath)
	if err := SaveHostSourcesIfUnchanged(s, core.Host{ID: "server", SSH: "alias"}, before); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(s.HostsPath)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("replaced host symlink")
	}
	info, err = os.Stat(target)
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("lost host file permissions: %v", err)
	}
	main, _ := Snapshot(s.MainPath)
	if string(main) != "# portable\n" {
		t.Fatal("host write modified main config")
	}
	if err := os.Remove(s.HostsPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(s.MainPath, s.HostsPath); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSources(s); err == nil || !strings.Contains(err.Error(), "same file") {
		t.Fatalf("accepted aliased source files: %v", err)
	}
	if err := SaveHostSourcesIfUnchanged(s, core.Host{ID: "new", SSH: "new"}, main); err == nil || !strings.Contains(err.Error(), "same file") {
		t.Fatalf("wrote through alias: %v", err)
	}
}
