package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestChildDragKeepsReleaseOutsidePane(t *testing.T) {
	m, term, _ := activeFake()
	l := m.layout()
	m.Update(tea.MouseClickMsg{X: l.tx + 2, Y: l.ty + 2, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: 0, Y: 1, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: 0, Y: 1, Button: tea.MouseLeft})
	if len(term.sent) != 3 {
		t.Fatalf("drag lost an event: %v", term.sent)
	}
	release, ok := term.sent[2].(tea.MouseReleaseMsg)
	if !ok || release.X != 0 || release.Y != 0 {
		t.Fatalf("release was not translated/clamped: %#v", term.sent[2])
	}
	if m.overlay != "" || m.childMouse {
		t.Fatal("release activated host chrome or retained capture")
	}
}

func TestCachedDiscoveryWaitsForFreshResultBeforeLaunch(t *testing.T) {
	m, service := testModel()
	m.cachedAt[m.hostID] = time.Now().Add(-time.Hour)
	if cmd := m.activate("alpha", false); cmd != nil || len(service.launches) != 0 || len(m.sessions) != 0 {
		t.Fatal("cached executable was launched before fresh discovery")
	}
	term := attachFake(m, m.hostID, "alpha")
	if cmd := m.activate("alpha", false); cmd != nil || m.current().Term != term {
		t.Fatal("cache refresh prevented access to an existing session")
	}
}

func TestPaletteMousePressFollowsActionIdentity(t *testing.T) {
	m, _, _ := activeFake()
	m.action("palette")
	m.Update(tea.MouseClickMsg{X: 2, Y: 5, Button: tea.MouseLeft})
	// Replacing the first result while a button is held must not activate it.
	m.input.SetValue("Quit")
	m.Update(tea.MouseReleaseMsg{X: 2, Y: 5, Button: tea.MouseLeft})
	if m.overlay != "palette" {
		t.Fatal("mouse release activated a different action at the same row")
	}
}
