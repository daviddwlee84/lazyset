package tui

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazyset/internal/core"
)

func TestRuntimeStartupDiagnosticsStaySeparateAndSanitized(t *testing.T) {
	m, _ := testModel()
	m.startup.Lines = []string{"preflight warning already printed by CLI"}
	m.startup.warn("Skipped missing\nforged\x1b[31mred\x1b[0m")
	var out bytes.Buffer
	if err := m.writeStartupWarnings(&out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Warning: Skipped missing forgedred\n" {
		t.Fatalf("unexpected diagnostics: %q", out.String())
	}
}

// LaunchSpec fails before pool.start: these tests never execute a real program,
// start the recurring tick, perform discovery, or touch user configuration.
func startupFixture(t *testing.T, opts Options, extra int) (*Model, *fakeHostService) {
	t.Helper()
	base, _ := testModel()
	cfg := base.cfg
	for n := 0; n < extra; n++ {
		id := fmt.Sprintf("extra-%d", n)
		cfg.Tools = append(cfg.Tools, core.Tool{ID: id, Name: id, Mode: "embedded", Command: []string{id}})
		cfg.Sets[0].Tools = append(cfg.Sets[0].Tools, id)
	}
	svc := &fakeHostService{launchErr: errors.New("fixture refused launch before process creation")}
	m := New(cfg, filepath.Join(t.TempDir(), "config.toml"), opts, svc)
	t.Cleanup(m.pool.close)
	return m, svc
}

func startupRows(m *Model) []core.Availability {
	rows := make([]core.Availability, 0, len(m.cfg.Tools))
	for _, tool := range m.cfg.Tools {
		state := "found"
		if tool.ID == "missing" {
			state = "missing"
		}
		rows = append(rows, core.Availability{ToolID: tool.ID, State: state, Path: "/fixture/" + tool.ID})
	}
	return rows
}

func startupProbe(m *Model, host string, err error) tea.Cmd {
	_, cmd := m.Update(probeMsg{Host: host, Generation: m.probeGeneration[host], Rows: startupRows(m), Err: err})
	return cmd
}

// Materialize a launch batch without delivering its results, so completion
// order and inflight concurrency remain under each test's control.
func startupResults(t *testing.T, cmd tea.Cmd) []startedMsg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		var results []startedMsg
		for _, child := range msg {
			results = append(results, startupResults(t, child)...)
		}
		return results
	case startedMsg:
		return []startedMsg{msg}
	default:
		t.Fatalf("unexpected command in deterministic startup test: %T", msg)
		return nil
	}
}

func startupDrain(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	results := startupResults(t, cmd)
	for delivered := 0; len(results) > 0; delivered++ {
		if delivered > 100 {
			t.Fatal("startup did not drain")
		}
		result := results[0]
		results = results[1:]
		_, next := m.Update(result)
		results = append(results, startupResults(t, next)...)
	}
}

func TestResolveStartupPreservesExplicitOrderAndDeduplicatesSets(t *testing.T) {
	m, _ := testModel()
	ids, warnings := ResolveStartup(m.cfg, Options{
		Tool: " beta ", Tools: []string{"alpha", "beta", "unknown", "unknown"},
		StartSets: []string{"second", "system", "absent", "absent"},
	})
	if want := []string{"beta", "alpha", "missing", "external"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("startup order = %v, want %v", ids, want)
	}
	if len(warnings) != 2 || !strings.Contains(warnings[0], `unknown tool "unknown"`) || !strings.Contains(warnings[1], `unknown set "absent"`) {
		t.Fatalf("warnings = %v", warnings)
	}
	if ids, warnings := ResolveStartup(m.cfg, Options{Set: "system"}); len(ids) != 0 || len(warnings) != 0 {
		t.Fatal("selecting a visible set unexpectedly became an eager launch")
	}
}

func TestStartupWaitsForFreshDiscoveryThenSkipsMissingAndExternal(t *testing.T) {
	m, svc := startupFixture(t, Options{Tools: []string{"missing", "external", "alpha", "beta"}}, 0)
	if cmd := m.pumpStartup(); cmd != nil || len(m.sessions) != 0 {
		t.Fatal("startup launched before discovery")
	}
	cmd := startupProbe(m, "local", nil)
	if len(m.startup.Inflight) != 2 || len(m.startup.Queue) != 0 {
		t.Fatalf("wrong pending state: %+v", m.startup)
	}
	if m.active != "local/alpha" || m.interact {
		t.Fatal("first eligible startup tool did not open in Observe")
	}
	startupDrain(t, m, cmd)
	if !reflect.DeepEqual(svc.launches, []string{"local/alpha", "local/beta"}) {
		t.Fatalf("launched unexpected tools: %v", svc.launches)
	}
	lines := strings.Join(m.startup.Lines, "\n")
	for _, want := range []string{"Skipped missing: missing", "Skipped external: external tools", "Failed to start local/alpha", "Failed to start local/beta"} {
		if !strings.Contains(lines, want) {
			t.Errorf("missing startup result %q in %s", want, lines)
		}
	}
	if cmd := startupProbe(m, "local", nil); cmd != nil {
		t.Fatal("refresh restarted consumed startup requests")
	}
}

func TestStartupCachedAndAuthenticationObservationsNeverAuthorizeLaunch(t *testing.T) {
	m, svc := startupFixture(t, Options{Tools: []string{"alpha"}}, 0)
	m.probing["local"] = true
	m.probeGeneration["local"] = 4
	m.Update(cachedMsg{Host: "local", Generation: 4, Rows: startupRows(m), At: time.Now()})
	if cmd := m.pumpStartup(); cmd != nil || len(m.sessions) != 0 {
		t.Fatal("cache started a tool while refresh was pending")
	}
	// Reference-only cache stays non-authoritative even when no probe is active.
	m.probing["local"] = false
	if cmd := m.pumpStartup(); cmd != nil || len(m.sessions) != 0 || !strings.Contains(m.startup.WaitReason, "cached") {
		t.Fatal("cached rows became launchable when probing stopped")
	}
	cmd := startupProbe(m, "local", errors.New("SSH authentication required"))
	if cmd != nil || len(svc.launches) != 0 || len(m.startup.Queue) != 1 {
		t.Fatal("failed fresh probe authorized launch from cached rows")
	}
	before := len(m.startup.Lines)
	_ = m.pumpStartup()
	if len(m.startup.Lines) != before {
		t.Fatal("unchanged authentication error repeated the same warning")
	}
	if !strings.Contains(strings.Join(m.startup.Lines, "\n"), "Use Connect or Refresh") {
		t.Fatal("waiting batch did not explain recovery")
	}
	// A probe from a cancelled generation must not clear the wait or launch.
	_, stale := m.Update(probeMsg{Host: "local", Generation: 3, Rows: startupRows(m)})
	if stale != nil || len(m.sessions) != 0 {
		t.Fatal("stale successful discovery started a tool")
	}
	startupDrain(t, m, startupProbe(m, "local", nil))
	if !reflect.DeepEqual(svc.launches, []string{"local/alpha"}) || len(m.startup.Queue) != 0 {
		t.Fatalf("fresh discovery did not resume exactly once: %v", svc.launches)
	}
}

func TestStartupIsBoundedAndRefillsOnlyCompletedSlots(t *testing.T) {
	m, svc := startupFixture(t, Options{Tools: []string{"alpha", "beta", "extra-0", "extra-1", "extra-2"}}, 3)
	cmd := startupProbe(m, "local", nil)
	if len(m.startup.Inflight) != startupConcurrency || len(m.sessions) != startupConcurrency || len(m.startup.Queue) != 2 {
		t.Fatalf("initial concurrency not bounded: %+v", m.startup)
	}
	results := startupResults(t, cmd)
	if len(results) != 3 || len(svc.launches) != 3 {
		t.Fatalf("unexpected effect count: %v", svc.launches)
	}
	// Complete the middle reservation first; exactly one queued item may enter.
	_, next := m.Update(results[1])
	if len(m.startup.Inflight) != 3 || len(m.startup.Queue) != 1 {
		t.Fatalf("completion failed to refill exactly one slot: %+v", m.startup)
	}
	additional := startupResults(t, next)
	if len(additional) != 1 || additional[0].Key != "local/extra-1" {
		t.Fatalf("wrong next reservation: %+v", additional)
	}
	// Duplicate delivery cannot release another slot.
	_, duplicate := m.Update(results[1])
	if duplicate != nil || len(m.startup.Queue) != 1 || len(m.startup.Inflight) != 3 {
		t.Fatal("duplicate completion released a second startup slot")
	}
	remaining := []startedMsg{results[0], results[2], additional[0]}
	for len(remaining) > 0 {
		message := remaining[0]
		remaining = remaining[1:]
		_, next := m.Update(message)
		if len(m.startup.Inflight) > startupConcurrency {
			t.Fatal("concurrency limit exceeded during drain")
		}
		remaining = append(remaining, startupResults(t, next)...)
	}
	if want := []string{"local/alpha", "local/beta", "local/extra-0", "local/extra-1", "local/extra-2"}; !reflect.DeepEqual(svc.launches, want) {
		t.Fatalf("launch reservation order changed: %v", svc.launches)
	}
	if len(m.startup.Inflight) != 0 || len(m.startup.Queue) != 0 {
		t.Fatal("completed startup retained pending work")
	}
}

func TestStartupTargetStaysFixedWhenUserChangesHosts(t *testing.T) {
	m, svc := startupFixture(t, Options{Host: "local", Tools: []string{"alpha"}}, 0)
	m.switchContext("remote", "system", false)
	selected := m.selected
	startupDrain(t, m, startupProbe(m, "local", nil))
	if !reflect.DeepEqual(svc.launches, []string{"local/alpha"}) {
		t.Fatalf("startup moved to the currently viewed host: %v", svc.launches)
	}
	if m.hostID != "remote" || m.selected != selected || m.active != "" {
		t.Fatal("background startup stole context from the user's host selection")
	}
}

func TestUserNavigationSuppressesStartupAutofocus(t *testing.T) {
	m, _ := startupFixture(t, Options{Tools: []string{"beta"}}, 0)
	press(m, tea.KeyPressMsg{Code: tea.KeyDown})
	selected := m.selected
	startupDrain(t, m, startupProbe(m, "local", nil))
	if m.startup.FocusAllowed || m.startup.Focused || m.active != "" || m.selected != selected {
		t.Fatal("startup overrode navigation performed while discovery was pending")
	}
	if _, ok := m.sessions["local/beta"]; !ok {
		t.Fatal("suppressing autofocus also suppressed the requested background start")
	}
}

func TestStopAllCancelsQueuedStartupWithoutAnyRunningSession(t *testing.T) {
	m, svc := startupFixture(t, Options{Tools: []string{"alpha", "beta"}}, 0)
	m.action("stop-all")
	if m.overlay != "confirm" || m.confirmAction != "stop-all" || !strings.Contains(strings.Join(m.confirmLines, " "), "2 queued") {
		t.Fatal("Stop all did not expose queued-only cancellation")
	}
	cmd := m.confirmApply()
	if cmd != nil {
		m.Update(cmd())
	}
	if !m.startup.Cancelled || len(m.startup.Queue) != 0 {
		t.Fatal("confirmed Stop all left queued startup requests")
	}
	for n := 0; n < 2; n++ {
		if cmd := startupProbe(m, "local", nil); cmd != nil {
			t.Fatal("late discovery revived cancelled startup")
		}
	}
	if len(m.sessions) != 0 || len(svc.launches) != 0 {
		t.Fatal("cancelled queued tools were launched")
	}
}

func TestLateStartupResultCannotReplaceNewerSessionGeneration(t *testing.T) {
	m, _ := startupFixture(t, Options{Tools: []string{"alpha"}}, 0)
	results := startupResults(t, startupProbe(m, "local", nil))
	if len(results) != 1 {
		t.Fatal("missing startup result")
	}
	old := results[0]
	// A manual restart reserves a newer generation before the old result arrives.
	newGeneration := m.pool.reserve(old.Key)
	replacement := attachFake(m, "local", "alpha")
	m.sessions[old.Key].Generation = newGeneration
	_, cmd := m.Update(old)
	if cmd != nil || m.sessions[old.Key].Term != replacement || m.sessions[old.Key].Generation != newGeneration || m.sessions[old.Key].Err != "" {
		t.Fatal("stale startup completion poisoned its replacement")
	}
	if len(m.startup.Inflight) != 0 || !strings.Contains(strings.Join(m.startup.Lines, "\n"), "Cancelled: local/alpha") {
		t.Fatal("replaced startup reservation did not finish as cancelled")
	}
	// A still-older duplicate cannot erase an actively tracked newer reservation.
	m.startup.Inflight[old.Key] = newGeneration
	m.Update(old)
	if got := m.startup.Inflight[old.Key]; got != newGeneration {
		t.Fatal("old duplicate removed a newer inflight reservation")
	}
}

func TestStopAllInvalidatesInflightStartupAndNeverPumpsTheRemainingQueue(t *testing.T) {
	m, svc := startupFixture(t, Options{Tools: []string{"alpha", "beta", "extra-0", "extra-1"}}, 2)
	results := startupResults(t, startupProbe(m, "local", nil))
	m.action("stop-all")
	cmd := m.confirmApply()
	if cmd != nil {
		m.Update(cmd())
	}
	for _, late := range results {
		_, next := m.Update(late)
		if next != nil {
			t.Fatal("late cancelled result restarted the queue")
		}
		if r := m.sessions[late.Key]; r.Starting || r.Err != "Launch cancelled." {
			t.Fatal("late result overwrote launch cancellation")
		}
	}
	if len(svc.launches) != 3 || len(m.startup.Queue) != 0 || len(m.startup.Inflight) != 0 {
		t.Fatalf("Stop all did not drain pending startup ownership: %+v", m.startup)
	}
	if _, ok := m.sessions["local/extra-1"]; ok {
		t.Fatal("queued tool started after Stop all")
	}
}
