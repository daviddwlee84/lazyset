package catalog_test

import (
	"testing"

	"github.com/daviddwlee84/lazyset/internal/catalog"
	"github.com/daviddwlee84/lazyset/internal/config"
)

func TestCatalogIsCompleteAndReferencesExistingTools(t *testing.T) {
	cfg := config.Defaults()
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	all, ok := cfg.Set(catalog.AllSetID)
	if !ok || !all.Builtin || all.Name != "All" || len(all.Tools) != len(cfg.Tools) {
		t.Fatalf("All should include the effective catalog: %+v", all)
	}
	for i, tool := range cfg.Tools {
		if all.Tools[i] != tool.ID {
			t.Fatalf("All tool %d: got %q, want %q", i, all.Tools[i], tool.ID)
		}
	}
	for _, id := range []string{catalog.AllSetID, "system", "gpu", "disk", "network", "containers", "logs", "files-git", "personal"} {
		if _, ok := cfg.Set(id); !ok || !catalog.IsBuiltinSet(id) {
			t.Errorf("missing built-in set %q", id)
		}
	}
	for id, executable := range map[string]string{"bottom": "btm", "superfile": "spf"} {
		tool, ok := cfg.Tool(id)
		if !ok || len(tool.Command) != 1 || tool.Command[0] != executable {
			t.Fatalf("%s executable should be %s", id, executable)
		}
	}
	gdu, _ := cfg.Tool("gdu")
	if gdu.Candidates[0] != "gdu-go" {
		t.Fatal("prefer Homebrew gdu-go over possibly GNU gdu")
	}
	for _, id := range []string{"lazygit", "yazi", "superfile", "lnav"} {
		tool, _ := cfg.Tool(id)
		if !tool.QToObserve {
			t.Errorf("%s should preserve its session on q by default", id)
		}
	}
	for _, set := range catalog.Sets() {
		if !set.Builtin {
			t.Errorf("builtin %s isn't marked builtin", set.ID)
		}
	}
}
