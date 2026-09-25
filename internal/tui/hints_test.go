package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazyset/internal/catalog"
)

func TestReturnHintRemainsVisibleAcrossWidthsAndToolPolicies(t *testing.T) {
	for _, width := range []int{120, 80, 60, 24} {
		for _, prefix := range []string{"ctrl+\\", "ctrl+g", "ctrl+space"} {
			for _, keys := range [][]string{{}, {"q"}, {"q", "Q", "ctrl+c", "esc", "f10"}} {
				m, _, _ := activeFake()
				m.width, m.height = width, 24
				m.cfg.Prefix = prefix
				m.current().Tool.ReturnKeys = keys
				m.status = strings.Repeat("unrelated status ", 20)
				lines := strings.Split(ansi.Strip(m.View().Content), "\n")
				footer := strings.TrimSpace(lines[len(lines)-1])
				sequence := prefixLabel(prefix) + " → Esc"
				if !strings.HasPrefix(footer, sequence) || ansi.StringWidth(footer) > width {
					t.Fatalf("width %d, prefix %s, keys %v: hidden return route: %q", width, prefix, keys, footer)
				}
				if width >= 60 && !strings.HasPrefix(footer, sequence+": lazyset") {
					t.Fatalf("return destination missing: %q", footer)
				}
				if len(keys) > 1 && strings.Contains(footer, "q/Q") && !strings.Contains(footer, strings.Join(keys, "/")+" return") {
					t.Fatalf("partially clipped return key segment: %q", footer)
				}
			}
		}
	}
}

func TestReturnHintTracksPendingInputState(t *testing.T) {
	m, _, _ := activeFake()
	press(m, prefixKey())
	if got := m.footer(60); !strings.HasPrefix(got, "Esc: lazyset") {
		t.Fatalf("prefix hint: %q", got)
	}
	press(m, char('v'))
	if got := m.footer(60); !strings.HasPrefix(got, "NEXT KEY → tool · then Ctrl+\\ → Esc: lazyset") {
		t.Fatalf("literal-next hint does not explain the consumed next key: %q", got)
	}
	if got := m.footer(24); !strings.HasPrefix(got, "NEXT KEY → tool") {
		t.Fatalf("narrow literal-next hint: %q", got)
	}
	press(m, char('q'))
	if got := m.footer(60); !strings.HasPrefix(got, "Ctrl+\\ → Esc: lazyset") {
		t.Fatalf("return hint not restored after literal key: %q", got)
	}
}

func TestFooterDoesNotAdvertiseDisabledMouse(t *testing.T) {
	m, _, _ := activeFake()
	if got := m.footer(160); !strings.Contains(got, "mouse [Quit]") {
		t.Fatalf("enabled mouse Quit hint missing: %q", got)
	}
	m.cfg.Mouse = false
	if got := m.footer(160); strings.Contains(got, "mouse") {
		t.Fatalf("disabled mouse still advertised: %q", got)
	}
}

func TestEmbeddedDetailsAlwaysExplainUniversalReturn(t *testing.T) {
	for _, keys := range [][]string{{}, {"q"}} {
		m, _, _ := activeFake()
		m.cfg.Prefix = "ctrl+g"
		m.cfg.Tools[0].ReturnKeys = keys
		text := strings.Join(m.detailLines(160), "\n")
		if !strings.Contains(text, "Ctrl+g → Esc: return to lazyset (Observe)") {
			t.Fatalf("embedded tool keys %v lost universal return: %s", keys, text)
		}
		m.cfg.Tools[0].Mode = "external"
		text = strings.Join(m.detailLines(160), "\n")
		if strings.Contains(text, "Ctrl+g") || !strings.Contains(text, "exit it to return") {
			t.Fatalf("external tool advertised an unavailable host shortcut: %s", text)
		}
	}
}

func TestExternalBuiltinDetailsOnlyAdvertiseNativeKeys(t *testing.T) {
	m, _ := testModel()
	tool := catalog.Tools()[0]
	tool.Mode = "external"
	m.cfg.Tools = append(m.cfg.Tools, tool)
	m.selected = tool.ID
	text := strings.Join(m.detailLines(160), "\n")
	if strings.Contains(text, "prefix") || strings.Contains(text, "Ctrl+\\") || !strings.Contains(text, "Native quit keys") {
		t.Fatalf("external built-in details mixed native and host shortcuts: %s", text)
	}
}

func TestSmallViewReportsActualInputMode(t *testing.T) {
	m, _, _ := activeFake()
	m.width, m.height = 24, 3
	if got := m.smallView(); !strings.Contains(got, "lazyset [interact]") || !strings.Contains(got, "Ctrl+\\ → Esc: lazyset") {
		t.Fatalf("small view misreported Interact: %q", got)
	}
	m.observe()
	if got := m.smallView(); !strings.Contains(got, "lazyset [observe]") {
		t.Fatalf("small view misreported Observe: %q", got)
	}
}

func TestBuiltinContextKeysReachChildBeforeGuardedQ(t *testing.T) {
	for _, id := range []string{"dev", "lazychezmoi", "htop", "translate"} {
		t.Run(id, func(t *testing.T) {
			m, term, _ := activeFake()
			for _, tool := range catalog.Tools() {
				if tool.ID == id {
					m.current().Tool = tool
				}
			}
			keys := []tea.KeyPressMsg{{Code: tea.KeyEscape}, {Code: 'c', Mod: tea.ModCtrl}, {Code: tea.KeyF10}, char('Q')}
			for i, key := range keys {
				if cmd := press(m, key); cmd != nil || !m.interact || m.overlay != "" || len(term.sent) != i+1 || !reflect.DeepEqual(term.sent[i], key) {
					t.Fatalf("native %s was intercepted: interact=%t, sent=%v", key.String(), m.interact, term.sent)
				}
			}
			press(m, char('q'))
			if id == "translate" {
				if !m.interact || len(term.sent) != len(keys)+1 {
					t.Fatal("translate acquired an unintended q guard")
				}
			} else if m.interact || len(term.sent) != len(keys) {
				t.Fatal("q protection no longer returns to Observe")
			}
			m.interact = true
			press(m, prefixKey())
			press(m, tea.KeyPressMsg{Code: tea.KeyEscape})
			if m.interact || m.current().Term != term || term.stops != 0 {
				t.Fatal("universal return changed child lifetime")
			}
		})
	}
}
