package tui

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"lazyset/internal/core"
)

func markInputTestExited(m *Model, term *fakeTerminal, code int) {
	term.once.Do(func() { close(term.done) })
	frame := term.Snapshot()
	frame.Exited, frame.ExitCode = true, code
	m.Update(frameMsg{Key: m.active, Term: term, Frame: frame})
}

func armLiteralNext(t *testing.T, m *Model) {
	t.Helper()
	press(m, prefixKey())
	press(m, char('v'))
	if !m.literalNext || m.literalTarget != m.active {
		t.Fatal("prefix v did not bind the next key to the current session")
	}
}

func TestReturnKeyPolicyRoutesCanonicalKeysAndLegacyOverrides(t *testing.T) {
	keys := []tea.KeyPressMsg{char('q'), char('Q'), {Code: 'c', Mod: tea.ModCtrl}, {Code: tea.KeyEscape}, {Code: tea.KeyF10}}
	for _, key := range keys {
		t.Run(key.String(), func(t *testing.T) {
			m, term, _ := activeFake()
			m.current().Tool.ReturnKeys = []string{key.String()}
			before := m.current()
			if cmd := press(m, key); cmd != nil || m.interact || m.current() != before || m.overlay != "" || len(term.sent) != 0 || term.stops != 0 {
				t.Fatalf("return key affected child or workspace lifetime: mode=%t overlay=%q sent=%v", m.interact, m.overlay, term.sent)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		keys   []string
		legacy bool
		guard  bool
	}{
		{"legacy true", nil, true, true},
		{"legacy false", nil, false, false},
		{"explicit empty wins", []string{}, true, false},
		{"uppercase stays distinct", []string{"Q"}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, term, _ := activeFake()
			m.current().Tool.ReturnKeys, m.current().Tool.QToObserve = tc.keys, tc.legacy
			press(m, char('q'))
			if m.interact == tc.guard || (len(term.sent) == 0) != tc.guard {
				t.Fatalf("wrong q policy: interact=%t sent=%v", m.interact, term.sent)
			}
		})
	}
}

func TestProtectedControlKeyRepeatCannotQuitWorkspace(t *testing.T) {
	m, term, _ := activeFake()
	m.current().Tool.ReturnKeys = []string{"ctrl+c"}
	key := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	press(m, key)
	key.IsRepeat = true
	for range 3 {
		if cmd := press(m, key); cmd != nil || m.overlay != "" || m.interact || term.stops != 0 || len(term.sent) != 0 {
			t.Fatal("holding a protected Ctrl+C escalated into a workspace quit")
		}
	}
}

func TestLiteralNextSendsOneGuardedKeyAndThenRestoresProtection(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{char('q'), char('Q'), {Code: 'c', Mod: tea.ModCtrl}, {Code: tea.KeyEscape}, prefixKey()} {
		t.Run(key.String(), func(t *testing.T) {
			m, term, _ := activeFake()
			m.current().Tool.ReturnKeys = []string{"q", "Q", "ctrl+c", "esc"}
			armLiteralNext(t, m)
			press(m, key)
			if !m.interact || m.literalNext || m.prefix || m.overlay != "" || len(term.sent) != 1 || !reflect.DeepEqual(term.sent[0], key) {
				t.Fatalf("literal next changed key or invoked outer action: sent=%v", term.sent)
			}
			press(m, char('q'))
			if m.interact || len(term.sent) != 1 {
				t.Fatal("literal-next bypass remained armed after one key")
			}
		})
	}
}

func TestLiteralNextCancelledByContextChangesAndChildExit(t *testing.T) {
	for _, transition := range []string{"host", "set", "tool", "overlay", "exit"} {
		t.Run(transition, func(t *testing.T) {
			m, term, _ := activeFake()
			other := attachFake(m, "local", "beta")
			armLiteralNext(t, m)
			switch transition {
			case "host":
				m.switchContext("remote", "system", false)
			case "set":
				m.switchContext("local", "second", false)
			case "tool":
				m.activate("beta", false)
			case "overlay":
				m.action("help")
			case "exit":
				markInputTestExited(m, term, 0)
			}
			if m.literalNext || m.literalTarget != "" {
				t.Fatal("context transition retained pending passthrough")
			}
			press(m, char('z'))
			if len(term.sent) != 0 || len(other.sent) != 0 {
				t.Fatal("a key after context change reached the old or new child")
			}
		})
	}
}

func TestLiteralNextPasteStaysWholeAndDisarms(t *testing.T) {
	for _, interact := range []bool{true, false} {
		m, term, _ := activeFake()
		m.interact = interact
		m.current().Tool.ReturnKeys = []string{"q", "ctrl+c", "esc"}
		armLiteralNext(t, m)
		paste := tea.PasteMsg{Content: "q\n\x03\x1b中文🙂"}
		m.Update(paste)
		if m.literalNext || m.literalTarget != "" || m.interact != interact || len(term.sent) != 1 || !reflect.DeepEqual(term.sent[0], paste) {
			t.Fatalf("paste was remapped or left passthrough armed: interact=%t sent=%v", interact, term.sent)
		}
		press(m, char('q'))
		if len(term.sent) != 1 || m.interact {
			t.Fatal("paste bypass affected the following guarded key")
		}
	}
}

func TestReopenRequiresExplicitActionAndConsumesEnter(t *testing.T) {
	for _, code := range []int{0, 7} {
		m, old, service := activeFake()
		attachFake(m, "local", "beta")
		markInputTestExited(m, old, code)
		closed := m.current()
		for _, event := range []tea.Msg{tea.BlurMsg{}, tea.FocusMsg{}} {
			if _, cmd := m.Update(event); cmd != nil {
				t.Fatal("terminal focus change requested a relaunch")
			}
		}
		m.move(1)
		m.move(-1)
		if cmd := m.activate("beta", false); cmd != nil {
			t.Fatal("viewing an existing sibling spawned a process")
		}
		if cmd := m.cycle(-1); cmd != nil || m.current() != closed || m.interact {
			t.Fatal("cycling back to exited session restarted or interacted with it")
		}
		if len(service.launches) != 0 || old.stops != 0 || len(old.sent) != 0 {
			t.Fatal("exit/navigation changed process lifetime")
		}
		cmd := press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		if cmd == nil || m.current() == closed || !m.current().Starting || m.interact || len(old.sent) != 0 {
			t.Fatal("explicit Enter did not request a fresh observed session")
		}
		generation := m.current().Generation
		if repeated := press(m, tea.KeyPressMsg{Code: tea.KeyEnter, IsRepeat: true}); repeated != nil || m.current().Generation != generation {
			t.Fatal("held Reopen Enter scheduled a duplicate start")
		}
		newTerm := newFakeTerminal()
		m.Update(startedMsg{Key: m.active, Generation: generation, Term: newTerm})
		if len(newTerm.sent) != 0 || m.interact {
			t.Fatal("Reopen Enter leaked into the new session")
		}
		press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		if !m.interact || len(newTerm.sent) != 0 {
			t.Fatal("Enter that entered the reopened session leaked to the child")
		}
	}
}

func TestReopenBlockedByUnavailableHostRetainsExitFrame(t *testing.T) {
	for _, state := range []string{"cached", "failed", "missing"} {
		t.Run(state, func(t *testing.T) {
			m, term, service := activeFake()
			markInputTestExited(m, term, 0)
			before := m.current()
			switch state {
			case "cached":
				m.cachedAt[m.hostID] = time.Now().Add(-time.Hour)
			case "failed":
				m.probeErrors[m.hostID] = "authentication required"
			case "missing":
				m.availability[m.hostID]["alpha"] = core.Availability{ToolID: "alpha", State: "missing"}
			}
			if cmd := m.action("reopen"); cmd != nil || m.current() != before || !m.current().Frame.Exited || len(service.launches) != 0 {
				t.Fatal("blocked reopen discarded exit frame or attempted a new launch")
			}
		})
	}
}

func TestExternalLaunchRefusesExistingEmbeddedAndPendingSessions(t *testing.T) {
	for _, starting := range []bool{false, true} {
		m, term, service := activeFake()
		m.current().Starting = starting
		if starting {
			m.current().Term = nil
		}
		if cmd := m.action("external"); cmd != nil || m.busy || len(service.launches) != 0 || term.stops != 0 || !strings.Contains(m.status, "Stop or Close") {
			t.Fatal("external launch duplicated or implicitly stopped embedded session")
		}
	}
}

func TestExternalLaunchRequiresFreshSuccessfulDiscovery(t *testing.T) {
	for _, state := range []string{"missing", "unknown", "unsupported", "cached", "probing", "failed"} {
		t.Run(state, func(t *testing.T) {
			m, service := testModel()
			m.selected = "alpha"
			switch state {
			case "cached":
				m.cachedAt[m.hostID] = time.Now().Add(-time.Hour)
			case "probing":
				m.probing[m.hostID] = true
			case "failed":
				m.probeErrors[m.hostID] = "authentication required"
			default:
				m.availability[m.hostID]["alpha"] = core.Availability{ToolID: "alpha", State: state}
			}
			if cmd := m.action("external"); cmd != nil || m.busy || len(service.launches) != 0 || len(m.sessions) != 0 {
				t.Fatalf("%s triggered external preparation/connection", state)
			}
		})
	}
}

func TestExternalPreparationIsAsyncAndDoesNotChangeToolMode(t *testing.T) {
	m, service := testModel()
	m.selected = "alpha"
	cmd := m.action("external")
	if cmd == nil || !m.busy || len(service.launches) != 0 || len(m.sessions) != 0 {
		t.Fatal("external preparation blocked the UI or registered an embedded session")
	}
	msg, ok := cmd().(externalReadyMsg)
	if !ok || msg.Err != nil || msg.Host.ID != "local" || msg.Tool.Mode != "external" || !reflect.DeepEqual(service.launches, []string{"local/alpha"}) {
		t.Fatalf("wrong prepared handoff: %#v", msg)
	}
	if configured, _ := m.cfg.Tool("alpha"); configured.Mode != "embedded" {
		t.Fatal("single external launch changed catalog configuration")
	}
	// An error never creates a terminal handoff command or implicitly retries.
	msg.Err = errors.New("SSH authentication required")
	if _, cmd := m.Update(msg); cmd != nil || m.busy || !strings.Contains(m.status, "authentication") {
		t.Fatal("authentication failure initiated a native handoff/retry")
	}
}

func TestExternalPreparationCancelledWhenHostChanges(t *testing.T) {
	m, _ := testModel()
	m.selected = "alpha"
	cmd := m.action("external")
	msg := cmd()
	m.switchContext("remote", "system", false)
	if _, cmd := m.Update(msg); cmd != nil || m.busy || !strings.Contains(m.status, "cancelled") {
		t.Fatal("late external preparation took over terminal for another host")
	}
}

func TestExternalBusyActionCannotPrepareSecondHandoff(t *testing.T) {
	m, service := testModel()
	m.selected = "alpha"
	if cmd := m.action("external"); cmd == nil {
		t.Fatal("first explicit external request was not accepted")
	}
	if cmd := m.action("external"); cmd != nil || len(service.launches) != 0 {
		t.Fatal("busy external request scheduled a second handoff")
	}
}

func TestVisibilityUsesProcessAndDiscoveryStatesWithoutLosingCurrentFrame(t *testing.T) {
	m, term, service := activeFake()
	m.availability[m.hostID]["beta"] = core.Availability{ToolID: "beta", State: "unsupported"}
	for id, want := range map[string]string{"alpha": "running", "external": "available", "missing": "missing", "beta": "other"} {
		if got := m.toolStatus(id); got != want {
			t.Fatalf("%s: status %s, want %s", id, got, want)
		}
	}
	m.action("visibility")
	press(m, tea.KeyPressMsg{Code: tea.KeyDown})
	press(m, tea.KeyPressMsg{Code: tea.KeyDown})
	press(m, char('o'))
	press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.overlay != "" || !reflect.DeepEqual(m.toolIDs(), []string{"missing"}) || m.current().Term != term || m.active != "local/alpha" || term.stops != 0 || len(service.launches) != 0 {
		t.Fatal("hiding Running lost/stopped current frame or launched a different tool")
	}
	if !strings.Contains(m.View().Content, "child screen") {
		t.Fatal("visibility filter hid the already displayed terminal")
	}
	m.action("visibility")
	press(m, char('a'))
	press(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if !reflect.DeepEqual(m.toolIDs(), []string{"missing"}) {
		t.Fatal("cancel applied an unsubmitted visibility draft")
	}
	m.action("visibility")
	press(m, char('a'))
	press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(m.toolIDs()) != 4 {
		t.Fatal("All states did not restore all tool choices")
	}
}

func TestVisibilityCachedAndFailedDiscoveryAreOtherButLiveStaysRunning(t *testing.T) {
	for _, state := range []string{"cached", "failed", "unknown"} {
		m, _, _ := activeFake()
		switch state {
		case "cached":
			m.cachedAt[m.hostID] = time.Now().Add(-time.Hour)
		case "failed":
			m.probeErrors[m.hostID] = "offline"
		case "unknown":
			m.availability[m.hostID]["missing"] = core.Availability{ToolID: "missing", State: "unknown"}
		}
		if m.toolStatus("alpha") != "running" || m.toolStatus("missing") != "other" {
			t.Fatalf("%s discovery misclassified missing or live tools", state)
		}
	}
}

func TestVisibilityPersistsPerViewWithoutProcessPersistence(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m, _, service := activeFake()
	m.visibility = visibilityFilter{Missing: true}
	m.switchContext("local", "second", false)
	if m.visibility != allVisible() {
		t.Fatal("new view inherited unrelated visibility")
	}
	m.visibility = visibilityFilter{Available: true}
	m.switchContext("local", "system", false)
	if m.visibility != (visibilityFilter{Missing: true}) {
		t.Fatal("returning to view lost visibility")
	}
	if err := m.saveState(); err != nil {
		t.Fatal(err)
	}
	restored := New(m.cfg, m.path, Options{}, service)
	restored.restoreState(Options{})
	if restored.visibility != (visibilityFilter{Missing: true}) || restored.active != "" || len(restored.sessions) != 0 {
		t.Fatal("visibility history was lost or restored a process")
	}
	restored.switchContext("local", "second", false)
	if restored.visibility != (visibilityFilter{Available: true}) || len(service.launches) != 0 {
		t.Fatal("saved secondary visibility was lost or started a tool")
	}
}
