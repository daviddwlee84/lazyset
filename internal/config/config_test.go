package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazyset/internal/catalog"
	"github.com/daviddwlee84/lazyset/internal/core"
)

func writeConfig(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadInheritsCatalogAndExplicitValues(t *testing.T) {
	path := writeConfig(t, `mouse = false
prefix = 'ctrl+g'
default_host = 'server'
default_set = 'work'
[[hosts]]
id = 'server'
name = 'Server'
ssh = 'user@server'
env = { LANG = 'C.UTF-8' }
[[tools]]
id = 'btop'
q_to_observe = false
[[tools]]
id = 'gdu'
command = ['/custom/disk-tool', 'path with spaces']
[[tools]]
id = 'editor'
command = ['nvim', 'file with spaces']
mode = 'external'
dir = '/somewhere'
env = { NVIM_APPNAME = 'minimal' }
[[sets]]
id = 'work'
name = 'Work'
tools = ['btop', 'editor']
`)
	cfg, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mouse || cfg.Prefix != "ctrl+g" || cfg.DefaultHost != "server" || cfg.DefaultSet != "work" {
		t.Fatalf("lost explicit settings: %+v", cfg)
	}
	btop, _ := cfg.Tool("btop")
	if btop.QToObserve || btop.Description == "" || len(btop.Command) == 0 {
		t.Fatalf("wrong builtin merge: %+v", btop)
	}
	editor, _ := cfg.Tool("editor")
	if editor.QToObserve || editor.Name != "editor" || editor.Env["NVIM_APPNAME"] != "minimal" || editor.Mode != "external" {
		t.Fatalf("wrong custom defaults: %+v", editor)
	}
	gdu, _ := cfg.Tool("gdu")
	if len(gdu.Candidates) != 0 || gdu.Command[1] != "path with spaces" {
		t.Fatalf("inherited stale executable candidates: %+v", gdu)
	}
	if _, ok := cfg.Set("system"); !ok {
		t.Fatal("lost builtin set")
	}
}

func TestMissingConfigurationAndDefaultsAreIndependent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "config.toml")
	a, err := Load(path, false)
	if err != nil {
		t.Fatal(err)
	}
	a.Tools[0].Command[0] = "changed"
	a.Sets[0].Tools[0] = "changed"
	b := Defaults()
	if b.Tools[0].Command[0] != "btop" || b.Sets[0].Tools[0] != "btop" {
		t.Fatal("defaults share mutable slices")
	}
	if _, err := Load(path, true); err == nil {
		t.Fatal("explicit missing path succeeded")
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("read created a config directory")
	}
}

func TestAllIncludesCustomToolsAndCopiesKeepTheirMembership(t *testing.T) {
	path := writeConfig(t, `default_set = 'all'
[[tools]]
id = 'btop'
q_to_observe = false
[[tools]]
id = 'custom-first'
command = ['first-tool']
[[tools]]
id = 'custom-second'
command = ['second-tool']
`)
	cfg, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	all, ok := cfg.Set(catalog.AllSetID)
	if !ok || !all.Builtin || cfg.DefaultSet != catalog.AllSetID {
		t.Fatalf("missing default All set: %+v", all)
	}
	want := make([]string, 0, len(cfg.Tools))
	for _, tool := range cfg.Tools {
		want = append(want, tool.ID)
	}
	if !reflect.DeepEqual(all.Tools, want) || !reflect.DeepEqual(all.Tools[len(all.Tools)-2:], []string{"custom-first", "custom-second"}) {
		t.Fatalf("All should preserve effective catalog order without duplicate overrides: %v", all.Tools)
	}
	copySet := all
	copySet.ID, copySet.Name, copySet.Builtin = "my-all", "My All", false
	if err := SaveSet(path, copySet); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("\n[[tools]]\nid = 'later-tool'\ncommand = ['later-tool']\n")...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	all, _ = cfg.Set(catalog.AllSetID)
	copied, _ := cfg.Set(copySet.ID)
	if !reflect.DeepEqual(all.Tools, append(want, "later-tool")) {
		t.Fatalf("All did not follow new tools: %v", all.Tools)
	}
	if copied.Builtin || !reflect.DeepEqual(copied.Tools, copySet.Tools) {
		t.Fatalf("copy should keep its original ordinary custom membership: %+v", copied)
	}
}

func TestInvalidConfiguration(t *testing.T) {
	tests := []struct{ name, text, want string }{
		{"typo", "moues = true", "invalid TOML"},
		{"empty prefix", "prefix = ''", "unsupported prefix"},
		{"unknown host", "default_host='elsewhere'", "default_host"},
		{"builtin set edit", "[[sets]]\nid='system'\ntools=['top']", "built in"},
		{"All set edit", "[[sets]]\nid='all'\ntools=['top']", "built in"},
		{"unknown tool", "[[sets]]\nid='mine'\ntools=['unknown']", "unknown tool"},
		{"repeat tool", "[[sets]]\nid='mine'\ntools=['top','top']", "repeats tool"},
		{"duplicate tool", "[[tools]]\nid='btop'\n[[tools]]\nid='btop'", "duplicate tool"},
		{"missing command", "[[tools]]\nid='custom'", "needs command"},
		{"empty builtin command", "[[tools]]\nid='btop'\ncommand=[]", "needs command"},
		{"bad mode", "[[tools]]\nid='btop'\nmode='pty'", "mode must"},
		{"host option injection", "[[hosts]]\nid='server'\nssh='-oProxyCommand=bad'", "OpenSSH alias"},
		{"host shell command", "[[hosts]]\nid='server'\nssh='server uname'", "OpenSSH alias"},
		{"local remote", "[[hosts]]\nid='local'\nssh='server'", "local cannot"},
		{"env name", "[[tools]]\nid='btop'\nenv={'bad=name'='value'}", "environment variable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.text), true)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want %s", err, tt.want)
			}
		})
	}
}

func TestXDGPathsIgnoreRelativeValuesWithoutWriting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "relative")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache-override"))
	path, err := Path("")
	if err != nil || path != filepath.Join(home, ".config", "lazyset", "config.toml") {
		t.Fatalf("path=%q err=%v", path, err)
	}
	state, err := StateDir()
	if err != nil || state != filepath.Join(home, ".local", "state", "lazyset") {
		t.Fatalf("state=%q err=%v", state, err)
	}
	cache, err := CacheDir()
	if err != nil || cache != filepath.Join(home, "cache-override", "lazyset") {
		t.Fatalf("cache=%q err=%v", cache, err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatal("path resolution created directories")
	}
	t.Setenv("HOME", "")
	if _, err := Path(""); err == nil {
		t.Fatal("missing HOME should not resolve into cwd")
	}
}

const patchFixture = `# File introduction
mouse = false # preference comment

[[tools]]
id = "custom"
command = ["nvim"]
description = """This string contains:
[[sets]]
id = 'not-a-set'
"""

# Keep this context for work.
[[sets]] # set declaration
id = 'work' # identity comment
name = "Original" # name comment
tools = [
  "btop", # old member comment
] # tools comment

# Keep this context for another set.
[[sets]]
id = 'other'
name = 'Other'
tools = ['top']

[tools.env]
NVIM_APPNAME = 'minimal' # unrelated nested table
`

func TestSetUpdatesPreserveUnrelatedTOMLAndComments(t *testing.T) {
	path := writeConfig(t, patchFixture)
	snapshot, err := Snapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	set := core.Set{ID: "work", Name: "工作 \"set\"", Tools: []string{"custom", "htop"}}
	if err := SaveSetIfUnchanged(path, set, snapshot); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, keep := range []string{"# File introduction", "mouse = false # preference comment", "id = 'work' # identity comment", "# name comment", "# tools comment", "# Keep this context for another set.", "id = 'not-a-set'", "NVIM_APPNAME = 'minimal' # unrelated nested table"} {
		if !strings.Contains(text, keep) {
			t.Errorf("lost %q in:\n%s", keep, text)
		}
	}
	if err := SaveSetIfUnchanged(path, set, snapshot); !errors.Is(err, ErrChanged) {
		t.Fatalf("stale snapshot got %v", err)
	}
	cfg, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := cfg.Set("work")
	if got.Name != set.Name || !reflect.DeepEqual(got.Tools, set.Tools) {
		t.Fatalf("set changed incorrectly: %+v", got)
	}
	if err := DeleteSet(path, "work"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), "# Keep this context for another set.") || !strings.Contains(string(data), "id = 'not-a-set'") {
		t.Fatal("delete removed unrelated content")
	}
	cfg, err = Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Set("work"); ok {
		t.Fatal("set still exists after deletion")
	}
	if _, ok := cfg.Set("other"); !ok {
		t.Fatal("other set deleted")
	}
}

func TestSaveAddsFieldsAndNewSetWithoutRewritingFile(t *testing.T) {
	path := writeConfig(t, "mouse = false\n[[sets]]\nid='empty' # last line")
	if err := SaveSet(path, core.Set{ID: "empty", Name: "Filled", Tools: []string{"top"}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveSet(path, core.Set{ID: "second", Name: "Second"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mouse {
		t.Fatal("lost unrelated setting")
	}
	if s, ok := cfg.Set("second"); !ok || len(s.Tools) != 0 {
		t.Fatalf("empty custom set: %+v", s)
	}
	if s, _ := cfg.Set("empty"); s.Name != "Filled" || len(s.Tools) != 1 {
		t.Fatalf("missing fields: %+v", s)
	}
}

func TestSetWriteSafety(t *testing.T) {
	t.Run("builtin unchanged", func(t *testing.T) {
		path := writeConfig(t, "# keep\n")
		for _, id := range []string{"system", catalog.AllSetID} {
			if err := SaveSet(path, core.Set{ID: id, Tools: []string{"top"}}); err == nil {
				t.Fatalf("edited builtin %s", id)
			}
			if err := DeleteSet(path, id); err == nil {
				t.Fatalf("deleted builtin %s", id)
			}
		}
		data, _ := os.ReadFile(path)
		if string(data) != "# keep\n" {
			t.Fatal("changed file")
		}
	})
	t.Run("default cannot disappear", func(t *testing.T) {
		path := writeConfig(t, "default_set='mine'\n[[sets]]\nid='mine'\ntools=[]\n")
		before, _ := os.ReadFile(path)
		if err := DeleteSet(path, "mine"); err == nil {
			t.Fatal("deleted referenced default set")
		}
		after, _ := os.ReadFile(path)
		if string(before) != string(after) {
			t.Fatal("failed validation changed file")
		}
	})
	t.Run("missing versus empty snapshot", func(t *testing.T) {
		path := writeConfig(t, "")
		if err := SaveSetIfUnchanged(path, core.Set{ID: "mine"}, nil); !errors.Is(err, ErrChanged) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("inline sets manual edit", func(t *testing.T) {
		path := writeConfig(t, "sets = [{id='mine',tools=['top']}]\n")
		if err := SaveSet(path, core.Set{ID: "mine", Tools: []string{"htop"}}); err == nil || !strings.Contains(err.Error(), "inline sets") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("missing file can be created", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "new", "config.toml")
		if err := SaveSetIfUnchanged(path, core.Set{ID: "mine", Tools: []string{"top"}}, nil); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := cfg.Set("mine"); !ok {
			t.Fatal("missing new set")
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
		if err := SaveSet(link, core.Set{ID: "mine", Tools: []string{"top"}}); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(link)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatal("replaced symlink")
		}
		info, err = os.Stat(target)
		if err != nil || info.Mode().Perm() != 0640 {
			t.Fatalf("lost permissions: %v %v", info, err)
		}
	})
}
