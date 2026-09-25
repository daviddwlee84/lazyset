package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"lazyset/internal/core"
	"lazyset/internal/session"
)

func addAllSet(m *Model) {
	m.cfg.Sets = append([]core.Set{{ID: "all", Name: "All", Builtin: true}}, m.cfg.Sets...)
}

func chooseMenuItem(t *testing.T, m *Model, id string) tea.Cmd {
	t.Helper()
	for index, row := range m.menuRows() {
		if row.ID == id {
			m.menuIndex = index
			return press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		}
	}
	t.Fatalf("menu %q has no item %q: %#v", m.overlay, id, m.menuRows())
	return nil
}

func applyEffect(m *Model, cmd tea.Cmd) {
	if cmd != nil {
		m.Update(cmd())
	}
}

func TestAllSetUsesExploreAndPreservesContext(t *testing.T) {
	m, term, _ := activeFake()
	addAllSet(m)
	m.filter = "CPU"
	m.action("explore")
	m.filter = "GPU"
	m.selected = "beta"
	m.action("explore")
	if m.explore || m.filter != "CPU" || m.active != "local/alpha" {
		t.Fatal("Explore did not retain the previous set context")
	}
	m.action("sets")
	chooseMenuItem(t, m, "all")
	if !m.explore || m.setID != "system" || m.filter != "GPU" || m.selected != "beta" {
		t.Fatalf("All did not reuse Explore: set=%s explore=%t filter=%q selected=%s", m.setID, m.explore, m.filter, m.selected)
	}
	m.filter = ""
	if got := len(m.toolIDs()); got != len(m.cfg.Tools) {
		t.Fatalf("All omitted catalog/custom tools: %d of %d", got, len(m.cfg.Tools))
	}
	m.action("hosts")
	chooseMenuItem(t, m, "remote")
	if !m.explore || m.hostID != "remote" || m.interact || term.stops != 0 {
		t.Fatal("host selection left All or stopped an existing session")
	}
}

func TestExplicitAndDefaultAllEnterExplore(t *testing.T) {
	base, service := testModel()
	addAllSet(base)
	for _, useDefault := range []bool{false, true} {
		t.Run(map[bool]string{false: "flag", true: "default"}[useDefault], func(t *testing.T) {
			cfg, opts := base.cfg, Options{Set: "all"}
			if useDefault {
				cfg.DefaultSet = "all"
				opts.Set = ""
			}
			m := New(cfg, base.path, opts, service)
			if !m.explore || len(m.toolIDs()) != len(cfg.Tools) {
				t.Fatalf("All did not expose every tool: explore=%t tools=%v", m.explore, m.toolIDs())
			}
		})
	}
}

func TestExplicitAllOverridesRestoredSet(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m, service := testModel()
	addAllSet(m)
	m.setID = "second"
	m.selected = "beta"
	m.filter = "GPU"
	if err := m.saveState(); err != nil {
		t.Fatal(err)
	}
	opts := Options{Set: "all"}
	restored := New(m.cfg, m.path, opts, service)
	restored.restoreState(opts)
	if !restored.explore || len(restored.toolIDs()) != len(restored.cfg.Tools) || restored.filter != "" {
		t.Fatalf("selection history overrode explicit All: explore=%t tools=%v filter=%q", restored.explore, restored.toolIDs(), restored.filter)
	}
}

func TestCommandPaletteAliasesAndInputOwnership(t *testing.T) {
	for _, opening := range []string{"colon", "space", "prefix-colon"} {
		t.Run(opening, func(t *testing.T) {
			m, term, _ := activeFake()
			switch opening {
			case "colon":
				m.observe()
				press(m, char(':'))
			case "space":
				m.observe()
				press(m, char(' '))
			case "prefix-colon":
				press(m, prefixKey())
				press(m, char(':'))
			}
			if m.overlay != "palette" || m.interact || m.input.Value() != "" {
				t.Fatalf("command entry failed: overlay=%q interact=%t input=%q", m.overlay, m.interact, m.input.Value())
			}
			for _, query := range []struct{ text, id string }{{"q", "quit"}, {"quit", "quit"}, {"close", "close"}, {"sessions", "sessions"}} {
				m.input.SetValue("")
				for _, c := range query.text {
					press(m, char(c))
				}
				rows := m.menuRows()
				if len(rows) == 0 || rows[0].ID != query.id {
					t.Fatalf("%q did not resolve to %q: %#v", query.text, query.id, rows)
				}
				if m.overlay != "palette" || m.confirmAction != "" || term.stops != 0 {
					t.Fatal("typing dispatched an action before Enter")
				}
			}
			m.input.SetValue("q")
			press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
			if m.overlay != "confirm" || m.confirmAction != "quit" || m.menuIndex != 0 {
				t.Fatal(":q bypassed the existing quit confirmation")
			}
			press(m, tea.KeyPressMsg{Code: tea.KeyEscape})
			if m.overlay != "" || term.stops != 0 || len(term.sent) != 0 || len(term.writes) != 0 {
				t.Fatal("command palette cancellation affected child input or lifetime")
			}
		})
	}
}

func TestInteractOwnsColonAndCloseKeys(t *testing.T) {
	m, term, _ := activeFake()
	for _, c := range ":qx " {
		m.current().Tool.QToObserve = false
		press(m, char(c))
	}
	if m.overlay != "" || !m.interact || len(term.sent) != 4 || term.stops != 0 {
		t.Fatal("normal child command keys invoked workspace commands")
	}
	press(m, prefixKey())
	press(m, char('x'))
	if m.overlay != "confirm" || m.confirmAction != "close" || m.confirmTarget != "local/alpha" || len(term.sent) != 4 {
		t.Fatal("prefix x did not request closing the current session without leaking input")
	}
}

func TestCloseCurrentCancelAndCleanup(t *testing.T) {
	m, term, _ := activeFake()
	other := attachFake(m, "local", "beta")
	m.pool.items["local/alpha"] = term
	m.pool.items["local/beta"] = other
	m.views["local/system/false"] = viewState{Selected: "alpha", Filter: "CPU", Active: "local/alpha"}
	m.views["remote/second/true"] = viewState{Selected: "beta", Active: "local/beta"}
	m.observe()
	press(m, char('x'))
	if m.overlay != "confirm" || m.confirmAction != "close" || m.confirmTarget != "local/alpha" {
		t.Fatal("x did not name the current session for confirmation")
	}
	press(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // Default is Cancel.
	if m.overlay != "" || term.stops != 0 || m.sessions["local/alpha"] == nil {
		t.Fatal("default close confirmation did not cancel safely")
	}
	press(m, char('x'))
	press(m, tea.KeyPressMsg{Code: tea.KeyTab})
	applyEffect(m, press(m, tea.KeyPressMsg{Code: tea.KeyEnter}))
	if term.stops == 0 || !ended(term) || m.sessions["local/alpha"] != nil || m.pool.items["local/alpha"] != nil {
		t.Fatal("confirmed close did not stop and remove session ownership")
	}
	if m.active != "" || m.interact || m.busy {
		t.Fatal("closed current session remained active or silently selected another")
	}
	if m.sessions["local/beta"] == nil || other.stops != 0 || m.pool.items["local/beta"] != other {
		t.Fatal("closing one session affected another session")
	}
	for _, view := range m.views {
		if view.Active == "local/alpha" {
			t.Fatal("closed session survived in a cached view")
		}
	}
	if m.views["local/system/false"].Filter != "CPU" || m.views["remote/second/true"].Active != "local/beta" {
		t.Fatal("closing a session discarded unrelated view context")
	}
	m.Update(frameMsg{Key: "local/alpha", Term: term, Frame: session.Frame{Content: "late frame"}})
	if m.sessions["local/alpha"] != nil || m.active != "" {
		t.Fatal("late frame resurrected a closed session")
	}
}

func TestClosePendingStartRejectsLateCompletion(t *testing.T) {
	m, _ := testModel()
	m.activate("alpha", false)
	key, generation := m.active, m.current().Generation
	unpublished := newFakeTerminal()
	m.pool.items[key] = unpublished
	press(m, char('x'))
	if m.overlay != "confirm" {
		t.Fatal("pending start was closed without confirmation")
	}
	applyEffect(m, m.confirmApply())
	_, lateCmd := m.Update(startedMsg{Key: key, Term: unpublished, Generation: generation})
	applyEffect(m, lateCmd)
	if !ended(unpublished) || m.sessions[key] != nil || m.pool.items[key] != nil || m.active != "" {
		t.Fatal("unpublished process survived closing its pending session")
	}
	if term, err := m.pool.start(key, generation, session.Spec{}); term != nil || err == nil || !strings.Contains(err.Error(), "closing") {
		t.Fatalf("closed start generation can still create a process: %v", err)
	}
	m.activate("alpha", false)
	if m.current() == nil || m.current().Generation <= generation || !m.current().Starting {
		t.Fatal("closed session could not be opened as a fresh generation")
	}
}

func TestCloseExitedAndFailedSessionsNeedsNoConfirmation(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "exited", true: "failed"}[failed], func(t *testing.T) {
			m, term, _ := activeFake()
			m.observe()
			if failed {
				m.current().Term = nil
				m.current().Err = "launch failed"
			} else {
				term.once.Do(func() { close(term.done) })
				m.current().Frame.Exited = true
				m.pool.items[m.active] = term
			}
			applyEffect(m, press(m, char('x')))
			if m.overlay == "confirm" || len(m.sessions) != 0 || m.active != "" || len(m.pool.items) != 0 {
				t.Fatal("inactive session was not dismissed directly")
			}
		})
	}
}

func TestSessionsCloseSelectedKeepsCurrentAndReturnsToList(t *testing.T) {
	m, current, _ := activeFake()
	other := attachFake(m, "remote", "beta")
	m.pool.items["remote/beta"] = other
	m.action("sessions")
	for index, row := range m.menuRows() {
		if row.ID == "remote/beta" {
			m.menuIndex = index
		}
	}
	press(m, char('x'))
	if m.overlay != "confirm" || m.confirmTarget != "remote/beta" {
		t.Fatal("Sessions x targeted current session instead of selected row")
	}
	press(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	rows := m.menuRows()
	if m.overlay != "sessions" || m.menuIndex >= len(rows) || rows[m.menuIndex].ID != "remote/beta" || other.stops != 0 {
		t.Fatal("cancelling a selected close lost the Sessions selection or stopped it")
	}
	press(m, char('x'))
	applyEffect(m, m.confirmApply())
	if m.overlay != "sessions" || m.sessions["remote/beta"] != nil || !ended(other) {
		t.Fatal("closing a session did not return to the updated session list")
	}
	if m.active != "local/alpha" || m.current().Term != current || current.stops != 0 || m.interact {
		t.Fatal("closing a selected background session disturbed the current session")
	}
}
