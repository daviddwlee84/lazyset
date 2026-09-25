package tui

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"lazyset/internal/core"
	"lazyset/internal/session"
)

type fakeTerminal struct {
	mu      sync.Mutex
	sent    []tea.Msg
	writes  [][]byte
	resizes [][2]int
	stops   int
	frame   session.Frame
	done    chan struct{}
	once    sync.Once
}

func newFakeTerminal() *fakeTerminal {
	return &fakeTerminal{done: make(chan struct{}), frame: session.Frame{Content: "child screen", Width: 80, Height: 24, Cursor: tea.NewCursor(0, 0), ExitCode: -1}}
}
func (f *fakeTerminal) Send(msg tea.Msg) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, msg)
	return nil
}
func (f *fakeTerminal) Write(data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, append([]byte(nil), data...))
	return nil
}
func (f *fakeTerminal) Resize(w, h int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizes = append(f.resizes, [2]int{w, h})
	return nil
}
func (f *fakeTerminal) Snapshot() session.Frame {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := f.frame
	if result.Cursor != nil {
		c := *result.Cursor
		result.Cursor = &c
	}
	return result
}
func (f *fakeTerminal) Stop() error {
	f.mu.Lock()
	f.stops++
	f.mu.Unlock()
	f.once.Do(func() { close(f.done) })
	return nil
}
func (f *fakeTerminal) Done() <-chan struct{} { return f.done }

type fakeHostService struct {
	launches  []string
	launchErr error
}

func (f *fakeHostService) Discover(context.Context, core.Host, []core.Tool) ([]core.Availability, error) {
	return nil, nil
}
func (f *fakeHostService) LaunchSpec(h core.Host, t core.Tool, path string) (session.Spec, error) {
	f.launches = append(f.launches, h.ID+"/"+t.ID)
	if f.launchErr != nil {
		return session.Spec{}, f.launchErr
	}
	return session.Spec{Command: []string{"/never-executed-in-model-tests"}}, nil
}
func (f *fakeHostService) ConnectCommand(context.Context, core.Host) (*exec.Cmd, error) {
	return nil, errors.New("connection should not execute in a model test")
}

func testModel() (*Model, *fakeHostService) {
	service := &fakeHostService{}
	cfg := core.Config{
		Prefix: "ctrl+\\", Mouse: true, DefaultHost: "local", DefaultSet: "system",
		Hosts: []core.Host{{ID: "local", Name: "Local 中文🙂"}, {ID: "remote", Name: "GPU host", SSH: "gpu"}},
		Tools: []core.Tool{
			{ID: "alpha", Name: "Alpha 中文🙂", Description: "CPU overview", Mode: "embedded", QToObserve: true},
			{ID: "missing", Name: "Missing", Mode: "embedded"},
			{ID: "external", Name: "External editor", Mode: "external"},
			{ID: "beta", Name: "Beta", Description: "GPU overview", Mode: "embedded"},
		},
		Sets: []core.Set{{ID: "system", Name: "System", Tools: []string{"alpha", "missing", "external", "beta"}}, {ID: "second", Name: "Second", Tools: []string{"beta", "alpha"}}},
	}
	m := New(cfg, "/never-read-in-model-tests", Options{}, service)
	for _, host := range cfg.Hosts {
		m.availability[host.ID] = map[string]core.Availability{}
		for _, tool := range cfg.Tools {
			state := "found"
			if tool.ID == "missing" {
				state = "missing"
			}
			m.availability[host.ID][tool.ID] = core.Availability{ToolID: tool.ID, State: state, Path: "/fake/" + tool.ID}
		}
	}
	return m, service
}

func attachFake(m *Model, hostID, toolID string) *fakeTerminal {
	h, _ := m.cfg.Host(hostID)
	tool, _ := m.cfg.Tool(toolID)
	term := newFakeTerminal()
	key := sessionKey(hostID, toolID)
	m.sessions[key] = &running{Key: key, Host: h, Tool: tool, Term: term, Frame: term.Snapshot()}
	return term
}
func activeFake() (*Model, *fakeTerminal, *fakeHostService) {
	m, service := testModel()
	term := attachFake(m, "local", "alpha")
	m.active = "local/alpha"
	m.selected = "alpha"
	m.interact = true
	return m, term, service
}
func press(m *Model, key tea.KeyPressMsg) tea.Cmd { _, cmd := m.Update(key); return cmd }
func char(c rune) tea.KeyPressMsg                 { return tea.KeyPressMsg{Code: c, Text: string(c)} }
func prefixKey() tea.KeyPressMsg                  { return tea.KeyPressMsg{Code: '\\', Mod: tea.ModCtrl} }

func TestQGuardAndLiteralBypass(t *testing.T) {
	for _, guard := range []bool{true, false} {
		t.Run(map[bool]string{true: "guarded", false: "passthrough"}[guard], func(t *testing.T) {
			m, term, _ := activeFake()
			m.current().Tool.QToObserve = guard
			press(m, char('q'))
			if m.interact == guard {
				t.Fatalf("q interaction state: interact=%t guard=%t", m.interact, guard)
			}
			if guard && len(term.sent) != 0 || !guard && len(term.sent) != 1 {
				t.Fatalf("q forwarding: %#v", term.sent)
			}
			if guard {
				press(m, tea.KeyPressMsg{Code: 'q', Text: "q", IsRepeat: true})
				if m.overlay != "" || term.stops != 0 {
					t.Fatal("repeated q turned observation into termination")
				}
				m.interact = true
				press(m, prefixKey())
				press(m, char('q'))
				if !m.interact || len(term.writes) != 1 || string(term.writes[0]) != "q" {
					t.Fatalf("literal q bypass: interact=%t writes=%q", m.interact, term.writes)
				}
			}
		})
	}
}

func TestPrefixAndPasteOwnership(t *testing.T) {
	m, term, _ := activeFake()
	press(m, prefixKey())
	if !m.prefix || len(term.sent) != 0 {
		t.Fatal("prefix reached child")
	}
	press(m, prefixKey())
	if m.prefix || len(term.writes) != 1 || string(term.writes[0]) != "\x1c" {
		t.Fatalf("literal prefix: %q", term.writes)
	}
	paste := "q\njkh/?中文🙂\x1c"
	m.Update(tea.PasteMsg{Content: paste})
	if len(term.sent) != 1 {
		t.Fatalf("paste forwarding: %#v", term.sent)
	}
	if got, ok := term.sent[0].(tea.PasteMsg); !ok || got.Content != paste {
		t.Fatalf("paste modified: %#v", term.sent[0])
	}
	if !m.interact || m.prefix {
		t.Fatal("paste invoked host shortcuts")
	}
	press(m, prefixKey())
	press(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.interact || m.prefix {
		t.Fatal("prefix Esc did not observe")
	}
	m.Update(tea.PasteMsg{Content: "ignored while observing"})
	if len(term.sent) != 1 {
		t.Fatal("observation paste reached child")
	}
}

func TestTypingOwnsHostSearchAndPalette(t *testing.T) {
	for _, surface := range []string{"filter", "palette", "prompt"} {
		t.Run(surface, func(t *testing.T) {
			m, term, _ := activeFake()
			m.observe()
			switch surface {
			case "filter":
				press(m, char('/'))
			case "palette":
				m.action("palette")
			case "prompt":
				m.prompt("set-name", "")
			}
			for _, c := range "jqkh/" {
				press(m, char(c))
			}
			m.Update(tea.PasteMsg{Content: "q文本"})
			if m.input.Value() != "jqkh/q文本" {
				t.Fatalf("typing lost or dispatched as action: %q", m.input.Value())
			}
			if len(term.sent) != 0 || len(term.writes) != 0 || m.prefix {
				t.Fatal("host typing reached child or prefix")
			}
			if surface == "filter" && m.filter != m.input.Value() {
				t.Fatalf("filter stale: %q", m.filter)
			}
		})
	}
}

func TestEnterConsumptionAndChildExit(t *testing.T) {
	m, service := testModel()
	cmd := press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil || m.active != "local/alpha" || !m.current().Starting || m.interact {
		t.Fatal("first Enter did not start observation")
	}
	// Deliver the asynchronous result without executing a real process.
	term := newFakeTerminal()
	m.Update(startedMsg{Key: m.active, Term: term, Generation: m.current().Generation})
	press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.interact || len(term.sent) != 0 {
		t.Fatal("Enter that entered interaction leaked to child")
	}
	press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(term.sent) != 1 {
		t.Fatal("subsequent Enter did not reach child")
	}
	if len(service.launches) != 0 {
		t.Fatalf("launch preparation ran on UI thread: %v", service.launches)
	}
	term.once.Do(func() { close(term.done) })
	_, cmd = m.Update(frameMsg{Key: m.active, Term: term, Frame: session.Frame{Exited: true, ExitCode: 7}})
	if cmd != nil || m.interact || !m.current().Frame.Exited || !strings.Contains(m.status, "exited (7)") {
		t.Fatalf("child exit dismissed workbench: %s", m.status)
	}
	press(m, char('q'))
	if m.overlay != "" || term.stops != 0 {
		t.Fatal("q after child exit quit host")
	}
}

func TestLaunchPreparationRunsOnlyInEffect(t *testing.T) {
	m, service := testModel()
	service.launchErr = errors.New("fake launch preparation failed")
	cmd := press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil || len(service.launches) != 0 {
		t.Fatal("launch preparation blocked Update")
	}
	msg := cmd()
	if len(service.launches) != 1 {
		t.Fatalf("effect did not prepare launch: %v", service.launches)
	}
	m.Update(msg)
	if m.current().Starting || !strings.Contains(m.current().Err, "fake launch preparation failed") {
		t.Fatal("asynchronous launch failure was not visible")
	}
}

func TestConfigReloadInvalidatesDiscoveryWithoutStoppingSessions(t *testing.T) {
	m, term, _ := activeFake()
	m.probeGeneration["local"] = 3
	cancelled := false
	m.probeCancel["local"] = func() { cancelled = true }
	updated := m.cfg
	updated.Tools = append([]core.Tool(nil), m.cfg.Tools...)
	updated.Tools[0].Command = []string{"new-executable"}
	m.applyConfig(updated)
	if !cancelled || m.probeGeneration["local"] <= 3 {
		t.Fatal("old configuration discovery remained valid")
	}
	if m.available("alpha").Path == "/fake/alpha" {
		t.Fatal("new command retained old resolved executable")
	}
	m.Update(probeMsg{Host: "local", Generation: 3, Rows: []core.Availability{{ToolID: "alpha", State: "found", Path: "/old-result"}}})
	if m.available("alpha").Path == "/old-result" {
		t.Fatal("pre-edit probe result was accepted after config reload")
	}
	if m.current().Term != term || term.stops != 0 {
		t.Fatal("configuration reload replaced a running session")
	}
}

func TestSetEditorBusyStatePreservesSubmittedDraft(t *testing.T) {
	m, _ := testModel()
	m.overlay = "setedit"
	m.draft = core.Set{ID: "mine", Name: "Mine", Tools: []string{"alpha", "beta"}}
	m.busy = true
	press(m, char('x'))
	press(m, char('d'))
	press(m, char('a'))
	if strings.Join(m.draft.Tools, ",") != "alpha,beta" || m.overlay != "setedit" {
		t.Fatal("pending save allowed draft edits that completion would discard")
	}
	m.overlay = "sets"
	m.menuIndex = 1
	press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.setID != "system" || m.overlay != "sets" {
		t.Fatal("pending set operation allowed a competing menu action")
	}
	m.click("menu:1")
	if m.setID != "system" || m.overlay != "sets" {
		t.Fatal("mouse menu bypassed busy state")
	}
}

func TestNegativeRestoredScrollIsClamped(t *testing.T) {
	m, _ := testModel()
	m.scroll = -100
	m.resetSelection()
	if m.scroll < 0 {
		t.Fatalf("negative persisted scroll remains: %d", m.scroll)
	}
	_ = m.View()
}

func TestStopAllCancelsUnpublishedStartedProcess(t *testing.T) {
	m, _ := testModel()
	_ = m.activate("alpha", false)
	key, generation := m.active, m.current().Generation
	term := newFakeTerminal()
	// Process creation finished inside the pool, but Bubble Tea has not yet
	// delivered its startedMsg. Stop all must still own this process.
	m.pool.items[key] = term
	m.action("stop-all")
	cmd := m.confirmApply()
	if cmd != nil {
		m.Update(cmd())
	}
	_, lateCmd := m.Update(startedMsg{Key: key, Term: term, Generation: generation})
	if lateCmd != nil {
		lateCmd()
	}
	if !ended(term) || term.stops == 0 {
		t.Fatal("process survived stop-all because its startedMsg was delayed")
	}
	if m.sessions[key].Starting || len(m.liveKeys()) != 0 {
		t.Fatal("cancelled start returned to running state")
	}
}

func TestPoolRejectsStartsAfterShutdownOrCancellation(t *testing.T) {
	for _, closePool := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelled", true: "closed"}[closePool], func(t *testing.T) {
			p := newPool()
			generation := p.reserve("local/alpha")
			if closePool {
				p.close()
			} else {
				p.cancel("local/alpha")
			}
			// An invalid spec proves the ownership check happens before Start could
			// validate or attempt any subprocess execution.
			term, err := p.start("local/alpha", generation, session.Spec{})
			if term != nil || err == nil || !strings.Contains(err.Error(), "closing") {
				t.Fatalf("stale start passed ownership gate: %v", err)
			}
		})
	}
}

func TestCacheCannotReplaceFreshDiscovery(t *testing.T) {
	m, _ := testModel()
	delete(m.availability, "local")
	m.probeGeneration["local"] = 2
	m.probing["local"] = true
	cached := cachedMsg{Host: "local", Generation: 1, At: time.Now().Add(-time.Hour), Rows: []core.Availability{{ToolID: "alpha", State: "found", Path: "/cached"}}}
	m.Update(cached)
	if m.availability["local"] != nil {
		t.Fatal("old generation cache was accepted")
	}
	cached.Generation = 2
	m.Update(cached)
	if m.available("alpha").Path != "/cached" || m.cachedAt["local"].IsZero() {
		t.Fatal("cache was not available as a dated provisional result")
	}
	m.Update(probeMsg{Host: "local", Generation: 2, Rows: []core.Availability{{ToolID: "alpha", State: "found", Path: "/fresh"}}})
	m.Update(cached)
	if m.available("alpha").Path != "/fresh" || !m.cachedAt["local"].IsZero() {
		t.Fatal("late cache overwrote live discovery")
	}
}

func TestStateRemembersSelectionWithoutRestoringProcesses(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m, service := testModel()
	attachFake(m, "remote", "beta")
	m.hostID = "remote"
	m.setID = "second"
	m.selected = "beta"
	m.filter = "GPU"
	m.active = "remote/beta"
	m.views["local/system/false"] = viewState{Selected: "alpha", Active: "local/alpha"}
	if err := m.saveState(); err != nil {
		t.Fatal(err)
	}
	restored := New(m.cfg, m.path, Options{}, service)
	restored.restoreState(Options{})
	if restored.hostID != "remote" || restored.setID != "second" || restored.selected != "beta" || restored.filter != "GPU" {
		t.Fatalf("selection not restored: host=%s set=%s tool=%s filter=%s", restored.hostID, restored.setID, restored.selected, restored.filter)
	}
	if restored.active != "" || len(restored.sessions) != 0 || len(service.launches) != 0 {
		t.Fatal("history automatically started a process")
	}
	for _, view := range restored.views {
		if view.Active != "" {
			t.Fatal("history retained dead process identity")
		}
	}
	opts := Options{Host: "local", Set: "system"}
	explicit := New(m.cfg, m.path, opts, service)
	explicit.restoreState(opts)
	if explicit.hostID != "local" || explicit.setID != "system" {
		t.Fatal("history overrode explicit flags")
	}
}

func TestFocusToolButtonKeyboardAndMouseShareBehavior(t *testing.T) {
	m, term, _ := activeFake()
	m.observe()
	for i, b := range m.buttons() {
		if b.ID == "focus-tool" {
			m.focus = i + 1
			break
		}
	}
	press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.interact || len(term.sent) != 0 {
		t.Fatal("keyboard activation of Interact button did not enter interaction")
	}
	// The same semantic action returns to observation without terminating it.
	m.click("action:focus-tool")
	if m.interact || term.stops != 0 {
		t.Fatal("Back button terminated or retained interaction")
	}
}

func TestDiscoveryGenerationAndStableSelection(t *testing.T) {
	m, _ := testModel()
	m.selected = "beta"
	m.probeGeneration["local"] = 2
	m.probing["local"] = true
	m.Update(probeMsg{Host: "local", Generation: 1, Rows: []core.Availability{{ToolID: "alpha", State: "missing"}}, Err: errors.New("stale failure")})
	if !m.probing["local"] || m.available("alpha").State != "found" || m.probeErrors["local"] != "" {
		t.Fatal("stale discovery modified current state")
	}
	m.Update(probeMsg{Host: "local", Generation: 2, Rows: []core.Availability{{ToolID: "alpha", State: "missing"}}, Err: errors.New("offline")})
	if m.probing["local"] || m.available("alpha").State != "found" || m.probeErrors["local"] != "offline" {
		t.Fatal("failed refresh discarded useful previous availability")
	}
	m.Update(probeMsg{Host: "local", Generation: 2, Rows: []core.Availability{{ToolID: "beta", State: "found"}, {ToolID: "alpha", State: "found"}}})
	if m.selected != "beta" || m.probeErrors["local"] != "" {
		t.Fatal("refresh replaced selection by a row index")
	}
}

func TestStaleFramesCannotOverwriteReplacement(t *testing.T) {
	m, current, _ := activeFake()
	old := newFakeTerminal()
	m.framePending = true
	m.Update(frameMsg{Key: m.active, Term: old, Frame: session.Frame{Content: "old", Exited: true}})
	if !m.interact || m.current().Term != current || m.current().Frame.Content == "old" {
		t.Fatal("old process frame replaced current session")
	}
}

func TestContextsAndToolSwitchesKeepSessions(t *testing.T) {
	m, local, service := activeFake()
	remote := attachFake(m, "remote", "beta")
	m.filter = "CPU"
	m.selected = "alpha"
	m.switchContext("remote", "second", false)
	m.active = "remote/beta"
	m.selected = "beta"
	m.interact = true
	m.switchContext("local", "system", false)
	if m.active != "local/alpha" || m.current().Term != local || m.filter != "CPU" || m.selected != "alpha" || m.interact {
		t.Fatalf("local context not restored: active=%q filter=%q selected=%q", m.active, m.filter, m.selected)
	}
	m.switchContext("remote", "second", false)
	if m.active != "remote/beta" || m.current().Term != remote || m.interact {
		t.Fatal("remote context not restored")
	}
	m.activate("beta", false)
	if len(service.launches) != 0 || local.stops != 0 || remote.stops != 0 {
		t.Fatal("view switching restarted or closed a session")
	}
}

func TestPrefixCyclesSkipMissingAndExternalTools(t *testing.T) {
	m, alpha, service := activeFake()
	beta := attachFake(m, "local", "beta")
	press(m, prefixKey())
	press(m, char('n'))
	if m.active != "local/beta" || m.interact || m.current().Term != beta {
		t.Fatalf("next selected unsuitable tool: %s", m.active)
	}
	press(m, prefixKey())
	press(m, char('p'))
	if m.active != "local/alpha" || m.current().Term != alpha {
		t.Fatalf("previous did not wrap to available tool: %s", m.active)
	}
	if len(service.launches) != 0 || alpha.stops != 0 || beta.stops != 0 {
		t.Fatal("cycle restarted stable sessions")
	}
}

func TestMouseOwnershipAndStalePress(t *testing.T) {
	m, term, _ := activeFake()
	layout := m.layout()
	m.Update(tea.MouseClickMsg{X: layout.tx + 2, Y: layout.ty + 3, Button: tea.MouseLeft, Mod: tea.ModShift})
	if len(term.sent) != 1 {
		t.Fatal("child mouse not forwarded")
	}
	got := term.sent[0].(tea.MouseClickMsg)
	if got.X != 2 || got.Y != 3 || got.Button != tea.MouseLeft || got.Mod != tea.ModShift {
		t.Fatalf("mouse translation damaged event: %+v", got)
	}
	m.action("help")
	m.Update(tea.MouseClickMsg{X: layout.tx + 2, Y: layout.ty + 3, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: layout.tx + 2, Y: layout.ty + 3, Button: tea.MouseLeft})
	if len(term.sent) != 1 {
		t.Fatal("overlay click reached child")
	}
	m.overlay = ""
	m.interact = true
	m.Update(tea.MouseClickMsg{X: 2, Y: 1, Button: tea.MouseLeft})
	m.Update(tea.WindowSizeMsg{Width: 110, Height: 30})
	m.Update(tea.MouseReleaseMsg{X: 2, Y: 1, Button: tea.MouseLeft})
	if m.overlay != "" {
		t.Fatal("release after resize activated stale header target")
	}
	m.Update(tea.MouseClickMsg{X: 2, Y: 1, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: layout.tx + 2, Y: layout.ty + 3, Button: tea.MouseLeft})
	if m.overlay != "" || len(term.sent) != 1 {
		t.Fatal("header drag clicked through into child")
	}
}

func TestClippedMouseTargetsAreInactive(t *testing.T) {
	m, _, _ := activeFake()
	m.width = 28
	for _, b := range m.buttons() {
		if b.X < m.width && b.X+len(b.Label)+2 > m.width {
			if got := m.hit(b.X, 1); got != "" {
				t.Fatalf("clipped header button active: %s", got)
			}
		}
	}
	m.width = 24
	m.height = 12
	m.confirm("stop", "local/alpha", []string{"target"})
	if got := m.hit(15, m.height-4); got != "" {
		t.Fatalf("clipped confirmation control active: %s", got)
	}
}

func TestUnicodeViewAndResizeStayWithinViewport(t *testing.T) {
	for _, surface := range []string{"child", "palette", "prompt", "filter"} {
		t.Run(surface, func(t *testing.T) {
			m, term, _ := activeFake()
			m.current().Frame.Content = "中文🙂é\n工具畫面"
			m.current().Frame.Cursor = tea.NewCursor(0, 0)
			switch surface {
			case "palette":
				m.action("palette")
			case "prompt":
				m.observe()
				m.prompt("set-name", "中文🙂")
			case "filter":
				m.observe()
				press(m, char('/'))
			}
			for _, size := range [][2]int{{120, 40}, {80, 24}, {35, 12}, {24, 9}, {10, 5}, {1, 1}, {0, 0}} {
				_, cmd := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				if cmd != nil {
					cmd()
				}
				view := m.View()
				lines := strings.Split(view.Content, "\n")
				if len(lines) > m.height {
					t.Fatalf("%v: too many lines (%d)", size, len(lines))
				}
				for _, line := range lines {
					if width := ansi.StringWidth(line); width > m.width {
						t.Fatalf("%v: line has %d cells, viewport %d", size, width, m.width)
					}
				}
				if view.Cursor != nil && (view.Cursor.X < 0 || view.Cursor.Y < 0 || view.Cursor.X >= m.width || view.Cursor.Y >= m.height) {
					t.Fatalf("%v: cursor outside viewport: %+v", size, view.Cursor)
				}
				last := term.resizes[len(term.resizes)-1]
				if last[0] < 1 || last[1] < 1 {
					t.Fatalf("nonpositive child resize: %v", last)
				}
			}
		})
	}
}
