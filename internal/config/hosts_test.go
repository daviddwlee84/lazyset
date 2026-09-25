package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazyset/internal/core"
)

func TestSaveHostPreservesExistingConfiguration(t *testing.T) {
	const existing = `# User-managed preferences
default_host = 'existing' # keep the current default
mouse = false

[[hosts]] # existing SSH host
id = 'existing'
name = 'Existing host'
ssh = 'existing-alias'
[hosts.env]
PATH = '/custom/bin:/usr/bin' # preserve environment
TOKEN = 'private-example'

[[tools]]
id = 'custom'
command = ['custom-tool']
description = '''This is documentation, not a table:
[[hosts]]
id = 'not-a-host'
'''

[[sets]]
id = 'work'
tools = ['custom']
# Last comment has no trailing newline`
	path := writeConfig(t, existing)
	snapshot, err := Snapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	h := core.Host{ID: "new-server", Name: "伺服器 \"GPU\"", SSH: "user@server", Env: map[string]string{"LANG": "C.UTF-8"}}
	if err := SaveHostIfUnchanged(path, h, snapshot); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, snapshot) {
		t.Fatal("adding a host rewrote existing TOML or comments")
	}
	cfg, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := cfg.Host(h.ID)
	if !ok || !reflect.DeepEqual(got, h) {
		t.Fatalf("new host was not saved accurately: %+v", got)
	}
	previous, _ := cfg.Host("existing")
	if previous.Name != "Existing host" || previous.SSH != "existing-alias" || previous.Env["TOKEN"] != "private-example" || previous.Env["PATH"] != "/custom/bin:/usr/bin" {
		t.Fatal("adding a host changed an existing host or its environment")
	}
	if cfg.DefaultHost != "existing" || cfg.Mouse {
		t.Fatal("adding a host changed user preferences")
	}
	if set, ok := cfg.Set("work"); !ok || !reflect.DeepEqual(set.Tools, []string{"custom"}) {
		t.Fatal("adding a host changed a custom set")
	}
}

func TestSaveHostRejectsInvalidOrExistingHostsWithoutWriting(t *testing.T) {
	valid := core.Host{ID: "new", Name: "New", SSH: "server-alias"}
	tests := []struct {
		name, want string
		host       core.Host
	}{
		{"local", "already exists", core.Host{ID: "local", Name: "Local", SSH: "server-alias"}},
		{"existing", "already exists", core.Host{ID: "existing", Name: "Replacement", SSH: "replacement-alias"}},
		{"invalid id", "must use", core.Host{ID: "bad id", Name: "Bad", SSH: "server-alias"}},
		{"missing alias", "needs one OpenSSH alias", core.Host{ID: "new", Name: "New"}},
		{"option injection", "needs one OpenSSH alias", core.Host{ID: "new", Name: "New", SSH: "-oProxyCommand=bad"}},
		{"multiple arguments", "needs one OpenSSH alias", core.Host{ID: "new", Name: "New", SSH: "server uname"}},
		{"control character", "needs one OpenSSH alias", core.Host{ID: "new", Name: "New", SSH: "server\n"}},
		{"invalid name", "single-line name", core.Host{ID: "new", Name: "New\nHost", SSH: "server-alias"}},
		{"invalid environment", "invalid environment", core.Host{ID: "new", Name: "New", SSH: "server-alias", Env: map[string]string{"BAD=NAME": "value"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, "# keep\n[[hosts]]\nid='existing'\nssh='existing-alias'\n")
			before, err := Snapshot(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := SaveHostIfUnchanged(path, tt.host, before); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want error containing %q", err, tt.want)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("failed host addition changed the config")
			}
		})
	}
	t.Run("inline hosts require manual editing", func(t *testing.T) {
		path := writeConfig(t, "hosts = [{id='existing',ssh='server'}] # keep\n")
		before, _ := Snapshot(path)
		if err := SaveHostIfUnchanged(path, valid, before); err == nil || !strings.Contains(err.Error(), "inline hosts") {
			t.Fatalf("got %v", err)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatal("failed inline host addition changed the config")
		}
	})
}

func TestSaveHostSnapshotAndFileSafety(t *testing.T) {
	h := core.Host{ID: "server", SSH: "server-alias"}
	t.Run("changed snapshot", func(t *testing.T) {
		path := writeConfig(t, "# original\n")
		before, _ := Snapshot(path)
		changed := []byte("# changed externally\nmouse = false\n")
		if err := os.WriteFile(path, changed, 0600); err != nil {
			t.Fatal(err)
		}
		if err := SaveHostIfUnchanged(path, h, before); !errors.Is(err, ErrChanged) {
			t.Fatalf("got %v", err)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(after, changed) {
			t.Fatal("host addition overwrote an intervening edit")
		}
	})
	t.Run("missing is distinct from empty", func(t *testing.T) {
		path := writeConfig(t, "")
		if err := SaveHostIfUnchanged(path, h, nil); !errors.Is(err, ErrChanged) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("missing config is created", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "new", "config.toml")
		if err := SaveHostIfUnchanged(path, h, nil); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path, true)
		if err != nil {
			t.Fatal(err)
		}
		if saved, ok := cfg.Host(h.ID); !ok || saved.SSH != h.SSH || saved.Name != h.ID {
			t.Fatalf("missing new host or default display name: %+v", saved)
		}
		if cfg.DefaultHost != "local" {
			t.Fatal("adding a host changed the default host")
		}
	})
	t.Run("symlink and permissions preserved", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target.toml")
		link := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(target, []byte("# managed\n"), 0640); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		before, _ := Snapshot(link)
		if err := SaveHostIfUnchanged(link, h, before); err != nil {
			t.Fatal(err)
		}
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatal("host addition replaced the symlink")
		}
		if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0640 {
			t.Fatal("host addition changed file permissions")
		}
		cfg, err := Load(target, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := cfg.Host(h.ID); !ok {
			t.Fatal("host addition did not update the symlink target")
		}
	})
}
