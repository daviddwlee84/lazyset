package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestPaneGeometryMatchesFramesChildSizeAndCursor(t *testing.T) {
	for _, size := range [][2]int{{120, 32}, {100, 24}, {80, 24}, {24, 9}} {
		m, term, _ := activeFake()
		m.noColor = true
		_, cmd := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		if cmd != nil {
			cmd()
		}
		g := m.layout()
		if g.tx != g.terminal.x+1 || g.ty != g.terminal.y+1 || g.tw != g.terminal.w-2 || g.th != g.terminal.h-2 {
			t.Fatalf("%v: terminal content is not inside frame: %+v", size, g)
		}
		if got := term.resizes[len(term.resizes)-1]; got != [2]int{g.tw, g.th} {
			t.Fatalf("%v: PTY size %v differs from content %dx%d", size, got, g.tw, g.th)
		}
		m.current().Frame.Content = strings.Repeat("X", size[0]*2)
		m.current().Frame.Cursor = tea.NewCursor(g.tw-1, g.th-1)
		v := m.View()
		lines := strings.Split(ansi.Strip(v.Content), "\n")
		if v.Cursor == nil || v.Cursor.X != g.tx+g.tw-1 || v.Cursor.Y != g.ty+g.th-1 {
			t.Fatalf("%v: cursor not translated into content: %+v", size, v.Cursor)
		}
		for y := g.ty; y < g.ty+g.th; y++ {
			if cell := ansi.Cut(lines[y], g.tx+g.tw, g.tx+g.tw+1); cell != "║" {
				t.Fatalf("%v: child overwrote right frame at row %d: %q", size, y, cell)
			}
		}
		m.current().Frame.Cursor = tea.NewCursor(g.tw, 0)
		if m.View().Cursor != nil {
			t.Fatalf("%v: cursor outside child content was shown on border", size)
		}
		if g.sidebar > 0 {
			if m.hit(g.list.x, g.ty) != "" || m.hit(g.list.x+g.list.w-1, g.ty) != "" {
				t.Fatal("sidebar border activated a tool")
			}
			if got := m.hit(g.list.x+1, g.ty); got != "tool:alpha" {
				t.Fatalf("sidebar content hit = %q", got)
			}
		}
	}
}

func TestFocusVisibleWithoutColor(t *testing.T) {
	m, _, _ := activeFake()
	m.noColor = true
	view := m.View().Content
	if !strings.Contains(view, "╔ INPUT") || !strings.Contains(view, "╭ TOOLS") {
		t.Fatal("Interact did not emphasize terminal frame")
	}
	m.observe()
	view = m.View().Content
	if !strings.Contains(view, "╔ TOOLS") || !strings.Contains(view, "╭ OBSERVE") {
		t.Fatal("Observe did not emphasize tool list frame")
	}
}

func TestObserveClickFocusesAndForwardsCompleteClick(t *testing.T) {
	for _, setting := range []string{"", "forward"} {
		m, term, _ := activeFake()
		m.observe()
		m.cfg.FocusClick = setting
		g := m.layout()
		click := tea.MouseClickMsg{X: g.tx + 3, Y: g.ty + 2, Button: tea.MouseLeft, Mod: tea.ModShift}
		m.Update(click)
		m.Update(tea.MouseReleaseMsg(click))
		if !m.interact || m.childMouse || len(term.sent) != 2 {
			t.Fatalf("%q: focus click not forwarded completely: interact=%t events=%#v", setting, m.interact, term.sent)
		}
		press, ok := term.sent[0].(tea.MouseClickMsg)
		if !ok || press.X != 3 || press.Y != 2 || press.Mod != tea.ModShift {
			t.Fatalf("%q: press not translated: %#v", setting, term.sent[0])
		}
		if release, ok := term.sent[1].(tea.MouseReleaseMsg); !ok || release.X != 3 || release.Y != 2 {
			t.Fatalf("%q: release not translated: %#v", setting, term.sent[1])
		}
	}
}

func TestFocusOnlyConsumesInitialClickAndDrag(t *testing.T) {
	m, term, _ := activeFake()
	m.observe()
	m.cfg.FocusClick = "focus-only"
	g := m.layout()
	click := tea.MouseClickMsg{X: g.tx + 2, Y: g.ty + 1, Button: tea.MouseLeft}
	m.Update(click)
	m.Update(tea.MouseMotionMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	if !m.interact || m.focusClickPending != "" || len(term.sent) != 0 {
		t.Fatalf("focus-only gesture leaked: %#v", term.sent)
	}
	m.Update(click)
	m.Update(tea.MouseReleaseMsg(click))
	if len(term.sent) != 2 {
		t.Fatalf("subsequent child click was not forwarded: %#v", term.sent)
	}
}

func TestObserveClickCannotFocusUnavailableChild(t *testing.T) {
	for _, state := range []string{"details", "starting", "exited", "failed"} {
		m, term, _ := activeFake()
		m.observe()
		switch state {
		case "details":
			m.active = ""
		case "starting":
			m.current().Starting = true
		case "exited":
			m.current().Frame.Exited = true
		case "failed":
			m.current().Err = "launch failed"
		}
		g := m.layout()
		click := tea.MouseClickMsg{X: g.tx + 1, Y: g.ty + 1, Button: tea.MouseLeft}
		m.Update(click)
		m.Update(tea.MouseReleaseMsg(click))
		if m.interact || len(term.sent) != 0 {
			t.Fatalf("%s: unavailable child became interactive", state)
		}
	}
}

func TestConfirmationPopupRetainsContextAndTrapsMouse(t *testing.T) {
	m, term, _ := activeFake()
	m.width, m.height = 120, 32
	m.noColor = true
	m.current().Frame.Content = "retained child context"
	m.requestClose(m.active, "")
	v := m.View()
	if !strings.Contains(v.Content, "retained child context") || !strings.Contains(v.Content, "Confirm: close") || v.Cursor != nil {
		t.Fatal("confirmation did not preserve the child view and hide the cursor")
	}
	g := m.confirmationLayout()
	if !g.pane.contains(g.cancel.x, g.cancel.y) || !g.pane.contains(g.accept.x+g.accept.w-1, g.accept.y) {
		t.Fatal("popup buttons escaped its frame")
	}
	if m.hit(g.cancel.x, g.cancel.y) != "cancel" || m.hit(g.accept.x, g.accept.y) != "confirm" || m.hit(2, 1) != "" {
		t.Fatal("popup hit regions do not match displayed controls")
	}
	m.Update(tea.MouseClickMsg{X: 2, Y: 1, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: 2, Y: 1, Button: tea.MouseLeft})
	if m.overlay != "confirm" || len(term.sent) != 0 {
		t.Fatal("outside popup click reached the workspace")
	}
	m.Update(tea.MouseClickMsg{X: g.cancel.x, Y: g.cancel.y, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: g.cancel.x, Y: g.cancel.y, Button: tea.MouseLeft})
	if m.overlay != "" || term.stops != 0 || len(term.sent) != 0 {
		t.Fatal("popup cancellation terminated or clicked through to child")
	}
}

func TestConfirmationPopupSessionsBackgroundAndNarrowResize(t *testing.T) {
	m, _, _ := activeFake()
	attachFake(m, "remote", "beta")
	m.width, m.height = 120, 32
	m.action("sessions")
	m.menuIndex = 1
	m.requestClose("remote/beta", "sessions")
	if view := m.View().Content; !strings.Contains(view, "lazyset · Sessions") || !strings.Contains(view, "GPU host / Beta") {
		t.Fatal("popup lost Sessions background or exact target")
	}
	for _, size := range [][2]int{{80, 24}, {35, 12}, {24, 9}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		g := m.confirmationLayout()
		if m.hit(g.cancel.x, g.cancel.y) != "cancel" || m.hit(g.accept.x, g.accept.y) != "confirm" {
			t.Fatalf("%v: resized popup controls are inaccessible", size)
		}
		v := m.View()
		for _, line := range strings.Split(v.Content, "\n") {
			if ansi.StringWidth(line) > m.width {
				t.Fatalf("%v: popup line exceeds terminal width", size)
			}
		}
	}
}
