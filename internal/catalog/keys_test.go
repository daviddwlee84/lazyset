package catalog

import (
	"slices"
	"testing"
)

func TestReturnDefaultsCoverCatalogWithNativeQuitMetadata(t *testing.T) {
	// Retain the full native metadata even though only lowercase q is guarded.
	wantNative := map[string][]string{
		"btop": {"q"}, "top": {"q"}, "ncdu": {"q"}, "bandwhich": {"q"},
		"htop": {"q", "f10"}, "bottom": {"q", "Q", "ctrl+c"},
		"gdu": {"q", "Q", "ctrl+c"}, "lazygit": {"q", "Q", "ctrl+c"},
		"glances": {"q", "esc", "ctrl+c"}, "dev": {"q", "esc", "ctrl+c"},
		"nvtop": {"q", "esc", "f10"}, "nvitop": {"q", "Q"},
		"lnav": {"q", "Q"}, "yazi": {"q", "Q"}, "lazydocker": {"q", "ctrl+c"},
		"superfile":   {"q", "esc", "Q"},
		"lazychezmoi": {"q", "ctrl+c"}, "lazyclash": {"q", "ctrl+c"},
		"lazymlflow": {"q", "ctrl+c"}, "lazypueue": {"q", "ctrl+c"},
		"lazycrontab": {"q", "ctrl+c"}, "exp": {"q", "ctrl+c"},
		"translate": {"ctrl+c", "esc"}, "k9s": {"ctrl+c", ":q", ":quit"},
	}
	tools := Tools()
	if len(tools) != len(wantNative) {
		t.Fatalf("got %d tools, want %d verified native contracts", len(tools), len(wantNative))
	}
	guarded := 0
	for _, tool := range tools {
		if len(tool.QuitKeys) == 0 || tool.QuitSource == "" || tool.QuitHint == "" {
			t.Errorf("missing native key contract for %s", tool.ID)
		}
		if keys := wantNative[tool.ID]; !slices.Equal(tool.QuitKeys, keys) {
			t.Errorf("%s native quit keys %v, want %v", tool.ID, tool.QuitKeys, keys)
		}
		var wantReturns []string
		if tool.ID != "translate" && tool.ID != "k9s" {
			wantReturns = []string{"q"}
			guarded++
		}
		if !slices.Equal(tool.ReturnKeys, wantReturns) {
			t.Errorf("%s return keys %v, want %v", tool.ID, tool.ReturnKeys, wantReturns)
		}
		if tool.QToObserve != slices.Contains(tool.ReturnKeys, "q") {
			t.Errorf("inconsistent legacy q flag for %s", tool.ID)
		}
		if tool.ID == "k9s" && (!slices.Contains(tool.QuitKeys, ":q") || slices.Contains(tool.ReturnKeys, ":q")) {
			t.Fatal("K9s command sequence must be informational only")
		}
	}
	if guarded != 22 {
		t.Fatalf("got %d q-guarded tools, want 22", guarded)
	}
	tools[0].ReturnKeys[0] = "changed"
	if Tools()[0].ReturnKeys[0] != "q" || tools[0].QuitKeys[0] != "q" {
		t.Fatal("native and effective key slices share storage")
	}
}
