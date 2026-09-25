package catalog

import (
	"reflect"
	"testing"
)

func TestPersonalToolsUseVerifiedTUIEntrypoints(t *testing.T) {
	want := map[string][]string{
		"dev":         {"dev", "tui"},
		"lazychezmoi": {"lazychezmoi", "tui"},
		"lazyclash":   {"lazyclash"},
		"lazymlflow":  {"lazymlflow"},
		"lazypueue":   {"lazypueue"},
		"lazycrontab": {"lazycrontab"},
		"translate":   {"translate"},
		"exp":         {"exp", "ui"},
	}
	tools := personalTools()
	if len(tools) != len(want) {
		t.Fatalf("got %d personal tools, want %d", len(tools), len(want))
	}
	for _, tool := range tools {
		if !reflect.DeepEqual(tool.Command, want[tool.ID]) {
			t.Errorf("%s command = %q, want %q", tool.ID, tool.Command, want[tool.ID])
		}
		if tool.Mode != "embedded" || len(tool.QuitKeys) == 0 || tool.Category != "Personal tools" || tool.InstallURL == "" || tool.Hint == "" {
			t.Errorf("incomplete personal tool contract: %+v", tool)
		}
	}
}

func TestPersonalSetIsAnIndependentBuiltinSnapshot(t *testing.T) {
	set := PersonalSet()
	if set.ID != "personal" || set.Name != "Personal tools" || !set.Builtin {
		t.Fatalf("personal set = %+v", set)
	}
	for i, tool := range personalTools() {
		if set.Tools[i] != tool.ID {
			t.Fatalf("set tool %d = %s, want %s", i, set.Tools[i], tool.ID)
		}
	}
	set.Tools[0] = "changed"
	if PersonalSet().Tools[0] != "dev" {
		t.Fatal("set mutation changed a subsequent catalog snapshot")
	}
}

func TestFileToolsKeepTheirDistinctDirectoryDefaults(t *testing.T) {
	want := map[string]string{"yazi": "~", "superfile": "~", "lazygit": ""}
	for _, tool := range Tools() {
		if dir, ok := want[tool.ID]; ok {
			if tool.Dir != dir {
				t.Errorf("%s dir = %q, want %q", tool.ID, tool.Dir, dir)
			}
			delete(want, tool.ID)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing file tools: %v", want)
	}
}
