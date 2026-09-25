package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func quitButton(t *testing.T, m *Model) button {
	t.Helper()
	for _, b := range m.buttons() {
		if b.ID == "quit" {
			return b
		}
	}
	t.Fatal("workspace has no Quit button")
	return button{}
}

func clickQuit(t *testing.T, m *Model) tea.Cmd {
	t.Helper()
	b := quitButton(t, m)
	m.Update(tea.MouseClickMsg{X: b.X, Y: b.Y, Button: tea.MouseLeft})
	_, cmd := m.Update(tea.MouseReleaseMsg{X: b.X, Y: b.Y, Button: tea.MouseLeft})
	return cmd
}

func requireQuit(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("quit did not produce an effect")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("quit effect produced %T", msg)
	}
}

func TestQuitButtonVisibleAndClickableAtSupportedWidths(t *testing.T) {
	for _, width := range []int{120, 80, 60, 24} {
		for _, interact := range []bool{false, true} {
			t.Run(fmt.Sprintf("width%d/interact%t", width, interact), func(t *testing.T) {
				m, _, _ := activeFake()
				m.width, m.height, m.interact, m.noColor = width, 9, interact, true
				m.cfg.Hosts[0].Name = strings.Repeat("很長的 host 👩🏽‍💻 ", 20)
				m.cfg.Sets[0].Name = strings.Repeat("very long set ", 20)
				line := strings.Split(m.View().Content, "\n")[0]
				if ansi.StringWidth(line) != width || !strings.HasSuffix(line, " [Quit]") {
					t.Fatalf("header did not reserve Quit space: %q", line)
				}
				b := quitButton(t, m)
				if b.X != width-6 || b.Y != 0 {
					t.Fatalf("unexpected Quit geometry: %+v", b)
				}
				for x := b.X; x < width; x++ {
					if got := m.hit(x, 0); got != "action:quit" {
						t.Fatalf("visible Quit cell %d hit %q", x, got)
					}
				}
				if got := m.hit(b.X-1, 0); got != "" {
					t.Fatalf("title spacing became clickable: %q", got)
				}
				clickQuit(t, m)
				if m.overlay != "confirm" || m.confirmAction != "quit" {
					t.Fatal("visible Quit button did not use quit confirmation")
				}
			})
		}
	}
}

func TestQuitButtonMouseAndTabUseExistingQuitFlow(t *testing.T) {
	for _, state := range []string{"empty", "exited", "live", "starting", "queued"} {
		for _, input := range []string{"mouse", "tab"} {
			t.Run(state+"/"+input, func(t *testing.T) {
				m, _ := testModel()
				var term *fakeTerminal
				switch state {
				case "exited", "live", "starting":
					term = attachFake(m, "local", "alpha")
					m.active = "local/alpha"
					m.interact = true
					if state == "exited" {
						_ = term.Stop()
					}
					if state == "starting" {
						m.current().Starting = true
					}
				case "queued":
					m.startup.Queue = []string{"alpha"}
				}
				var cmd tea.Cmd
				if input == "mouse" {
					cmd = clickQuit(t, m)
				} else {
					m.observe()
					for range m.buttons() {
						press(m, tea.KeyPressMsg{Code: tea.KeyTab})
					}
					if m.buttons()[m.focus-1].ID != "quit" {
						t.Fatal("Tab did not reach Quit")
					}
					cmd = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
				}
				if state == "empty" || state == "exited" {
					requireQuit(t, cmd)
					if m.overlay != "" {
						t.Fatal("idle workspace needed confirmation")
					}
					return
				}
				if cmd != nil || m.overlay != "confirm" || m.confirmAction != "quit" || m.confirmIndex != 0 {
					t.Fatal("Quit did not default to cancellation")
				}
				if term != nil && (len(term.sent) != 0 || term.stops != 0) {
					t.Fatal("Quit click reached or stopped the child before confirmation")
				}
				if cmd := press(m, tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil || m.overlay != "" || m.startup.Cancelled {
					t.Fatal("default Enter did not cancel Quit safely")
				}
				clickQuit(t, m)
				g := m.confirmationLayout()
				m.Update(tea.MouseClickMsg{X: g.accept.x, Y: g.accept.y, Button: tea.MouseLeft})
				_, cmd = m.Update(tea.MouseReleaseMsg{X: g.accept.x, Y: g.accept.y, Button: tea.MouseLeft})
				requireQuit(t, cmd)
				if !m.startup.Cancelled || len(m.startup.Queue) != 0 {
					t.Fatal("confirmed Quit did not cancel queued launches")
				}
			})
		}
	}
}

func TestQuitButtonHonorsModalAndMouseOwnership(t *testing.T) {
	for _, blocker := range []string{"help", "confirm", "busy", "mouse-off"} {
		t.Run(blocker, func(t *testing.T) {
			m, term, _ := activeFake()
			switch blocker {
			case "help":
				m.action("help")
			case "confirm":
				m.requestClose(m.active, "")
			case "busy":
				m.busy = true
			case "mouse-off":
				m.cfg.Mouse = false
			}
			overlay, action := m.overlay, m.confirmAction
			if cmd := clickQuit(t, m); cmd != nil || m.overlay != overlay || m.confirmAction != action || len(term.sent) != 0 {
				t.Fatal("blocked Quit click escaped its input context")
			}
		})
	}
	t.Run("child-drag", func(t *testing.T) {
		m, term, _ := activeFake()
		l, b := m.layout(), quitButton(t, m)
		m.Update(tea.MouseClickMsg{X: l.tx + 2, Y: l.ty + 2, Button: tea.MouseLeft})
		_, cmd := m.Update(tea.MouseReleaseMsg{X: b.X, Y: b.Y, Button: tea.MouseLeft})
		if cmd != nil || m.overlay != "" || len(term.sent) != 2 || m.childMouse {
			t.Fatal("dragging from child to Quit activated the host")
		}
		if _, ok := term.sent[1].(tea.MouseReleaseMsg); !ok {
			t.Fatal("child drag lost its release")
		}
	})
	t.Run("quit-drag", func(t *testing.T) {
		m, term, _ := activeFake()
		l, b := m.layout(), quitButton(t, m)
		m.Update(tea.MouseClickMsg{X: b.X, Y: b.Y, Button: tea.MouseLeft})
		_, cmd := m.Update(tea.MouseReleaseMsg{X: l.tx + 2, Y: l.ty + 2, Button: tea.MouseLeft})
		if cmd != nil || m.overlay != "" || len(term.sent) != 0 {
			t.Fatal("dragging off Quit activated it or leaked a child release")
		}
	})
	t.Run("resize", func(t *testing.T) {
		m, term, _ := activeFake()
		b := quitButton(t, m)
		m.Update(tea.MouseClickMsg{X: b.X, Y: b.Y, Button: tea.MouseLeft})
		m.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
		b = quitButton(t, m)
		_, cmd := m.Update(tea.MouseReleaseMsg{X: b.X, Y: b.Y, Button: tea.MouseLeft})
		if cmd != nil || m.overlay != "" || len(term.sent) != 0 {
			t.Fatal("release after resize activated stale Quit press")
		}
	})
}
