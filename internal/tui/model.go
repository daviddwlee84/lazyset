// Package tui hosts terminal tools without making the host and child compete
// for the real terminal's input reader.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazyset/internal/catalog"
	"github.com/daviddwlee84/lazyset/internal/config"
	"github.com/daviddwlee84/lazyset/internal/core"
	"github.com/daviddwlee84/lazyset/internal/host"
	"github.com/daviddwlee84/lazyset/internal/session"
)

type Options struct {
	Host, Set, Tool            string
	Tools, StartSets, Warnings []string
	Sources                    config.Sources
}

type hostService interface {
	Discover(context.Context, core.Host, []core.Tool) ([]core.Availability, error)
	LaunchSpec(core.Host, core.Tool, string) (session.Spec, error)
	ConnectCommand(context.Context, core.Host) (*exec.Cmd, error)
}

type running struct {
	Key        string
	Host       core.Host
	Tool       core.Tool
	Term       terminal
	Frame      session.Frame
	Starting   bool
	Err        string
	Generation uint64
}

type viewState struct {
	Selected, Filter, Active string
	Scroll                   int
	Visibility               *visibilityFilter `json:",omitempty"`
}
type probeMsg struct {
	Host       string
	Generation int
	Rows       []core.Availability
	Err        error
}
type startedMsg struct {
	Key        string
	Term       terminal
	Err        error
	Generation uint64
}
type frameMsg struct {
	Key   string
	Term  terminal
	Frame session.Frame
}
type tickMsg time.Time
type stoppedMsg struct {
	Keys    []string
	Restart string
	Err     error
	Close   string
}
type handoffMsg struct {
	Kind, Host, Tool string
	Err              error
	Config           *core.Config
}
type editSetMsg struct {
	Draft    core.Set
	Snapshot []byte
	Err      error
	Rename   bool
}
type savedSetMsg struct {
	ID     string
	Config core.Config
	Err    error
}
type connectReadyMsg struct {
	Host    string
	Command *exec.Cmd
	Err     error
}
type editorReadyMsg struct {
	Command *exec.Cmd
	Err     error
	Hosts   bool
}
type deleteReadyMsg struct {
	Set      core.Set
	Snapshot []byte
	Err      error
}
type externalReadyMsg struct {
	Host core.Host
	Tool core.Tool
	Spec session.Spec
	Err  error
}
type cachedMsg struct {
	Host       string
	Generation int
	Rows       []core.Availability
	At         time.Time
}

type Model struct {
	cfg                                  core.Config
	path                                 string
	sources                              config.Sources
	service                              hostService
	pool                                 *pool
	width, height                        int
	hostID, setID, selected, active      string
	explore, interact, prefix, filtering bool
	focus, scroll                        int
	filter                               string
	views                                map[string]viewState
	availability                         map[string]map[string]core.Availability
	probeGeneration                      map[string]int
	probeCancel                          map[string]context.CancelFunc
	probing                              map[string]bool
	probeErrors                          map[string]string
	cachedAt                             map[string]time.Time
	sessions                             map[string]*running
	framePending                         bool
	status                               string
	startup                              startupBatch
	visibility, visibilityDraft          visibilityFilter
	literalNext                          bool
	literalTarget                        string
	returnGuardKey                       string
	focusClickPending                    string
	overlay                              string
	menuIndex                            int
	input                                textinput.Model
	promptPurpose                        string
	draft                                core.Set
	draftSnapshot                        []byte
	confirmAction, confirmTarget         string
	confirmReturn                        string
	confirmIndex                         int
	hostInputs                           [2]textinput.Model
	hostField                            int
	hostSnapshot                         []byte
	confirmLines                         []string
	busy                                 bool
	pressed                              string
	childMouse                           bool
	pendingG                             bool
	noColor                              bool
}

func New(cfg core.Config, path string, opts Options, service hostService) *Model {
	in := textinput.New()
	in.Prompt = "> "
	in.CharLimit = 256
	m := &Model{cfg: cfg, path: path, service: service, pool: newPool(), width: 100, height: 30,
		hostID: opts.Host, setID: opts.Set, input: in,
		views: map[string]viewState{}, availability: map[string]map[string]core.Availability{},
		probeGeneration: map[string]int{}, probeCancel: map[string]context.CancelFunc{},
		probing: map[string]bool{}, probeErrors: map[string]string{}, sessions: map[string]*running{},
		cachedAt: map[string]time.Time{},
		status:   "Choose a tool. Enter opens a view; Enter again interacts.", noColor: os.Getenv("NO_COLOR") != ""}
	if m.hostID == "" {
		m.hostID = cfg.DefaultHost
	}
	if m.setID == "" {
		m.setID = cfg.DefaultSet
	}
	if m.setID == catalog.AllSetID {
		m.setID = m.browseSet()
		m.explore = true
	}
	m.sources = opts.Sources
	if m.sources.MainPath == "" {
		m.sources, _ = config.ResolveSources(path, "", false, false)
	}
	m.visibility = allVisible()
	ids, warnings := ResolveStartup(cfg, opts)
	m.startup = startupBatch{HostID: m.hostID, Queue: ids, Inflight: map[string]uint64{}, FocusAllowed: len(ids) > 0}
	m.startup.Lines = append(append(append([]string(nil), cfg.Warnings...), opts.Warnings...), warnings...)
	if len(ids) > 0 && opts.Set == "" {
		m.explore = true
	}
	m.resetSelection()
	return m
}

func Run(cfg core.Config, path string, opts Options) error {
	mgr := host.NewManager()
	defer mgr.Close()
	m := New(cfg, path, opts, mgr)
	m.restoreState(opts)
	defer m.pool.close()
	defer func() {
		for _, cancel := range m.probeCancel {
			cancel()
		}
	}()
	_, err := tea.NewProgram(m).Run()
	warningErr := m.writeStartupWarnings(os.Stderr)
	stateErr := m.saveState()
	return errors.Join(err, warningErr, stateErr)
}

func (m *Model) Init() tea.Cmd { return tea.Batch(m.refresh(), tick()) }
func tick() tea.Cmd            { return tea.Tick(time.Second/30, func(t time.Time) tea.Msg { return tickMsg(t) }) }

func (m *Model) refresh() tea.Cmd {
	h, ok := m.cfg.Host(m.hostID)
	if !ok {
		return nil
	}
	if cancel := m.probeCancel[h.ID]; cancel != nil {
		cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	m.probeCancel[h.ID] = cancel
	m.probeGeneration[h.ID]++
	gen := m.probeGeneration[h.ID]
	m.probing[h.ID] = true
	tools := append([]core.Tool(nil), m.cfg.Tools...)
	svc := m.service
	probe := func() tea.Msg {
		defer cancel()
		rows, err := svc.Discover(ctx, h, tools)
		if err == nil {
			_ = writeDiscoveryCache(h, tools, rows)
		}
		return probeMsg{h.ID, gen, rows, err}
	}
	cache := func() tea.Msg { rows, at := readDiscoveryCache(h, tools); return cachedMsg{h.ID, gen, rows, at} }
	return tea.Batch(cache, probe)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, v.Width), max(1, v.Height)
		if m.width < 24 || m.height < 9 {
			m.observe()
		}
		m.pressed = ""
		m.focusClickPending = ""
		m.input.SetWidth(max(1, m.width-8))
		for i := range m.hostInputs {
			m.hostInputs[i].SetWidth(max(1, m.width-6))
		}
		l := m.layout()
		terms := make([]terminal, 0, len(m.sessions))
		for _, r := range m.sessions {
			if r.Term != nil && !ended(r.Term) {
				terms = append(terms, r.Term)
			}
		}
		return m, func() tea.Msg {
			for _, t := range terms {
				_ = t.Resize(l.tw, l.th)
			}
			return nil
		}
	case probeMsg:
		if m.probeGeneration[v.Host] != v.Generation {
			return m, nil
		}
		m.probing[v.Host] = false
		if v.Err != nil {
			m.probeErrors[v.Host] = v.Err.Error()
			if m.availability[v.Host] == nil {
				m.availability[v.Host] = rowsByID(v.Rows)
			}
		} else {
			m.probeErrors[v.Host] = ""
			m.availability[v.Host] = rowsByID(v.Rows)
			delete(m.cachedAt, v.Host)
		}
		m.resetSelection()
		return m, m.pumpStartup()
	case cachedMsg:
		if m.probeGeneration[v.Host] == v.Generation && m.probing[v.Host] && m.availability[v.Host] == nil && !v.At.IsZero() {
			m.availability[v.Host] = rowsByID(v.Rows)
			m.cachedAt[v.Host] = v.At
		}
		return m, nil
	case startedMsg:
		m.completeStartup(v)
		r := m.sessions[v.Key]
		if r == nil || r.Generation != v.Generation {
			return m, m.pumpStartup()
		}
		r.Starting = false
		r.Term = v.Term
		if r.Term != nil {
			l := m.layout()
			_ = r.Term.Resize(l.tw, l.th)
		}
		if v.Err != nil {
			r.Err = v.Err.Error()
			if m.active == v.Key {
				m.status = r.Err
			}
		}
		m.resetSelection()
		return m, m.pumpStartup()
	case tickMsg:
		cmds := []tea.Cmd{tick()}
		if r := m.current(); r != nil && r.Term != nil && !m.framePending {
			m.framePending = true
			key, t := r.Key, r.Term
			cmds = append(cmds, func() tea.Msg { return frameMsg{key, t, t.Snapshot()} })
		}
		return m, tea.Batch(cmds...)
	case frameMsg:
		m.framePending = false
		if r := m.sessions[v.Key]; r != nil && r.Term == v.Term {
			wasExited := r.Frame.Exited
			r.Frame = v.Frame
			if m.active == v.Key && v.Frame.Exited && !wasExited {
				m.observe()
				m.status = fmt.Sprintf("%s / %s exited (%d). :restart reopens it; x closes the session.", r.Host.Name, r.Tool.Name, v.Frame.ExitCode)
			}
		}
		m.resetSelection()
		return m, nil
	case connectReadyMsg:
		if v.Err != nil {
			m.busy = false
			m.status = v.Err.Error()
			return m, nil
		}
		return m, tea.ExecProcess(v.Command, func(err error) tea.Msg { return handoffMsg{Kind: "SSH connection", Host: v.Host, Err: err} })
	case externalReadyMsg:
		m.busy = false
		if v.Err != nil {
			m.status = v.Err.Error()
			return m, nil
		}
		if v.Host.ID != m.hostID {
			m.status = "External launch cancelled after host changed."
			return m, nil
		}
		return m, m.external(v.Host, v.Tool, v.Spec)
	case editorReadyMsg:
		if v.Err != nil {
			m.busy = false
			m.status = v.Err.Error()
			return m, nil
		}
		sources := m.sources
		return m, tea.ExecProcess(v.Command, func(err error) tea.Msg {
			if err != nil {
				return handoffMsg{Kind: "Editor", Err: err}
			}
			cfg, err := config.LoadSources(sources)
			if err != nil {
				return handoffMsg{Kind: "Configuration validation", Err: err}
			}
			return handoffMsg{Kind: "Editor", Config: &cfg}
		})
	case deleteReadyMsg:
		m.busy = false
		if v.Err != nil {
			m.status = v.Err.Error()
			return m, nil
		}
		m.draftSnapshot = v.Snapshot
		m.confirm("delete-set", v.Set.ID, []string{"Delete custom set " + v.Set.Name + "?", "Tools and running sessions are retained."})
		return m, nil
	case stoppedMsg:
		m.busy = false
		if v.Err != nil {
			m.status = v.Err.Error()
		} else {
			m.status = "Session stopped."
		}
		if v.Close != "" && v.Err == nil {
			m.forgetSession(v.Close)
			m.status = "Session closed. Other tools keep running."
			return m, nil
		}
		if v.Restart != "" {
			if r := m.sessions[v.Restart]; r != nil {
				return m, m.activate(r.Tool.ID, true)
			}
		}
		return m, nil
	case handoffMsg:
		m.busy = false
		m.observe()
		if v.Err != nil {
			m.status = v.Kind + ": " + v.Err.Error()
		} else {
			m.status = v.Kind + " finished."
		}
		if v.Config != nil {
			changed := m.applyConfig(*v.Config)
			m.status = "Configuration reloaded. Existing sessions keep their launch settings."
			if changed {
				return m, m.refresh()
			}
		}
		if v.Kind == "SSH connection" && v.Err == nil && v.Host == m.hostID {
			return m, m.refresh()
		}
		return m, nil
	case editSetMsg:
		m.busy = false
		if v.Err != nil {
			m.status = v.Err.Error()
			return m, nil
		}
		m.draft = v.Draft
		m.draftSnapshot = v.Snapshot
		m.menuIndex = 0
		if v.Rename {
			return m, m.prompt("set-name", m.draft.Name)
		}
		m.overlay = "setedit"
		return m, nil
	case hostDraftMsg:
		m.busy = false
		if v.Err != nil {
			m.status = v.Err.Error()
			return m, nil
		}
		return m, m.initHostForm(v.Snapshot)
	case hostSavedMsg:
		m.busy = false
		if v.Err != nil {
			m.status = v.Err.Error()
			return m, nil
		}
		m.applyConfig(v.Config)
		m.overlay = "hosts"
		for i, h := range m.cfg.Hosts {
			if h.ID == v.ID {
				m.menuIndex = i
				break
			}
		}
		m.status = "Host saved. Enter selects it; Connect handles SSH authentication."
		return m, nil
	case savedSetMsg:
		m.busy = false
		if v.Err != nil {
			m.status = v.Err.Error()
			return m, nil
		}
		m.overlay = ""
		changed := m.applyConfig(v.Config)
		if _, ok := m.cfg.Set(v.ID); ok {
			m.switchContext(m.hostID, v.ID, false)
		}
		m.status = "Set saved."
		if changed {
			return m, m.refresh()
		}
		return m, nil
	case tea.KeyReleaseMsg:
		if v.String() == m.returnGuardKey {
			m.returnGuardKey = ""
		}
		return m, nil
	case tea.KeyPressMsg:
		m.startup.FocusAllowed = false
		return m, m.key(v)
	case tea.PasteMsg:
		if m.literalNext {
			m.literalNext = false
			m.literalTarget = ""
			if m.overlay == "" {
				m.send(v)
				return m, nil
			}
		}
		if m.overlay == "hostedit" && !m.busy {
			var cmd tea.Cmd
			m.hostInputs[m.hostField], cmd = m.hostInputs[m.hostField].Update(v)
			return m, cmd
		}
		if m.overlay == "palette" || m.overlay == "prompt" || m.filtering {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(v)
			m.syncFilter()
			return m, cmd
		}
		if m.interact {
			m.send(v)
		}
		return m, nil
	case tea.MouseMsg:
		switch v.(type) {
		case tea.MouseClickMsg, tea.MouseWheelMsg:
			m.startup.FocusAllowed = false
		}
		return m, m.mouse(v)
	default:
		if m.overlay == "hostedit" {
			var cmd tea.Cmd
			m.hostInputs[m.hostField], cmd = m.hostInputs[m.hostField].Update(msg)
			return m, cmd
		}
		if m.overlay == "palette" || m.overlay == "prompt" || m.filtering {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func rowsByID(rows []core.Availability) map[string]core.Availability {
	out := map[string]core.Availability{}
	for _, v := range rows {
		out[v.ToolID] = v
	}
	return out
}
func contains(ids []string, id string) bool {
	for _, s := range ids {
		if s == id {
			return true
		}
	}
	return false
}
func sessionKey(hostID, toolID string) string { return hostID + "/" + toolID }
func (m *Model) current() *running            { return m.sessions[m.active] }
func (m *Model) available(id string) core.Availability {
	if a, ok := m.availability[m.hostID][id]; ok {
		return a
	}
	return core.Availability{ToolID: id, State: "unknown", Reason: "Not checked yet"}
}
func (m *Model) toolIDs() []string {
	var ids []string
	if m.explore {
		for _, t := range m.cfg.Tools {
			ids = append(ids, t.ID)
		}
	} else if s, ok := m.cfg.Set(m.setID); ok {
		ids = append(ids, s.Tools...)
	}
	q := strings.ToLower(m.filter)
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if t, ok := m.cfg.Tool(id); ok && m.visibility.shows(m.toolStatus(id)) && strings.Contains(strings.ToLower(t.Name+" "+t.Description+" "+t.Category+" "+id), q) {
			out = append(out, id)
		}
	}
	return out
}
func (m *Model) resetSelection() {
	ids := m.toolIDs()
	if !contains(ids, m.selected) {
		m.selected = ""
		if len(ids) > 0 {
			m.selected = ids[0]
		}
	}
	m.clampScroll()
}
func (m *Model) index() int {
	for i, id := range m.toolIDs() {
		if id == m.selected {
			return i
		}
	}
	return 0
}
func (m *Model) move(delta int) {
	ids := m.toolIDs()
	if len(ids) == 0 {
		m.selected = ""
		return
	}
	i := max(0, min(len(ids)-1, m.index()+delta))
	m.selected = ids[i]
	m.clampScroll()
	m.startup.FocusAllowed = false
}
func (m *Model) clampScroll() {
	n := max(1, m.layout().th)
	i := m.index()
	m.scroll = max(0, min(m.scroll, max(0, len(m.toolIDs())-n)))
	if i < m.scroll {
		m.scroll = i
	}
	if i >= m.scroll+n {
		m.scroll = i - n + 1
	}
}
func (m *Model) contextKey() string { return fmt.Sprintf("%s/%s/%t", m.hostID, m.setID, m.explore) }

func (m *Model) browseSet() string {
	if m.cfg.DefaultSet != catalog.AllSetID {
		if _, ok := m.cfg.Set(m.cfg.DefaultSet); ok {
			return m.cfg.DefaultSet
		}
	}
	if _, ok := m.cfg.Set("system"); ok {
		return "system"
	}
	for _, s := range m.cfg.Sets {
		if s.ID != catalog.AllSetID {
			return s.ID
		}
	}
	return ""
}
func (m *Model) switchContext(h, s string, explore bool) {
	visibility := m.visibility
	m.views[m.contextKey()] = viewState{Selected: m.selected, Filter: m.filter, Active: m.active, Scroll: m.scroll, Visibility: &visibility}
	if s == catalog.AllSetID {
		s = m.setID
		explore = true
		if s == catalog.AllSetID {
			s = m.browseSet()
		}
	}
	m.hostID = h
	m.setID = s
	m.explore = explore
	m.observe()
	m.focus = 0
	m.filtering = false
	m.startup.FocusAllowed = false
	v := m.views[m.contextKey()]
	m.selected = v.Selected
	m.filter = v.Filter
	m.active = v.Active
	m.scroll = v.Scroll
	m.visibility = allVisible()
	if v.Visibility != nil {
		m.visibility = *v.Visibility
	}
	if r := m.current(); r != nil && r.Host.ID != h {
		m.active = ""
	}
	m.resetSelection()
}
func (m *Model) observe() {
	wasInteractive := m.interact
	m.interact = false
	m.prefix = false
	m.pressed = ""
	m.childMouse = false
	m.focusClickPending = ""
	m.literalNext = false
	m.literalTarget = ""
	if r := m.current(); wasInteractive && r != nil && contains(m.toolIDs(), r.Tool.ID) {
		m.selected = r.Tool.ID
	}
	m.focus = 0
	m.resetSelection()
}
func (m *Model) send(msg tea.Msg) {
	if r := m.current(); r != nil && r.Term != nil && !ended(r.Term) {
		if err := r.Term.Send(msg); err != nil {
			m.status = err.Error()
		}
	}
}
func (m *Model) literal(b []byte) {
	if r := m.current(); r != nil && r.Term != nil && !ended(r.Term) {
		if err := r.Term.Write(b); err != nil {
			m.status = err.Error()
		}
	}
}

func (m *Model) activate(id string, force bool) tea.Cmd {
	m.startup.FocusAllowed = false
	if m.busy {
		m.status = "An operation is already in progress."
		return nil
	}
	t, ok := m.cfg.Tool(id)
	if !ok {
		return nil
	}
	h, ok := m.cfg.Host(m.hostID)
	if !ok {
		return nil
	}
	m.observe()
	m.selected = id
	m.focus = 0
	key := sessionKey(h.ID, t.ID)
	if r := m.sessions[key]; r != nil && !force {
		m.active = key
		return nil
	}
	if !m.cachedAt[h.ID].IsZero() {
		m.status = "Waiting for current discovery. Cached availability is for reference; existing sessions remain usable."
		return nil
	}
	a := m.available(id)
	if a.State != "found" {
		m.status = t.Name + ": " + a.State + ". " + a.Reason + "  See Explore / tool details for installation."
		m.overlay = "toolinfo"
		m.menuIndex = 0
		return nil
	}
	if m.probeErrors[h.ID] != "" {
		m.status = "Refresh or Connect first; this host's last discovery failed."
		return nil
	}
	l := m.layout()
	svc := m.service
	if t.Mode == "external" {
		m.busy = true
		return func() tea.Msg {
			spec, err := svc.LaunchSpec(h, t, a.Path)
			spec.Width = l.tw
			spec.Height = l.th
			return externalReadyMsg{h, t, spec, err}
		}
	}
	return m.launchEmbedded(h, t, a.Path, true)
}

func (m *Model) cycle(delta int) tea.Cmd {
	ids := m.toolIDs()
	if len(ids) == 0 {
		return nil
	}
	start := m.index()
	for n := 1; n <= len(ids); n++ {
		i := (start + delta*n + len(ids)*2) % len(ids)
		id := ids[i]
		t, ok := m.cfg.Tool(id)
		if ok && t.Mode == "embedded" && m.available(id).State == "found" {
			m.selected = id
			m.clampScroll()
			return m.activate(id, false)
		}
	}
	m.status = "No available embedded tools in this view."
	return nil
}

func (m *Model) external(h core.Host, t core.Tool, spec session.Spec) tea.Cmd {
	if m.busy {
		return nil
	}
	m.busy = true
	m.observe()
	cmd := exec.Command(spec.Command[0], spec.Command[1:]...)
	cmd.Dir = spec.Dir
	cmd.Env = mergedEnv(spec.Env)
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return handoffMsg{Kind: "External tool", Host: h.ID, Tool: t.ID, Err: err} })
}

func mergedEnv(overrides map[string]string) []string {
	values := map[string]string{}
	for _, s := range os.Environ() {
		k, v, ok := strings.Cut(s, "=")
		if ok {
			values[k] = v
		}
	}
	for k, v := range overrides {
		values[k] = v
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+values[k])
	}
	return out
}

func (m *Model) applyConfig(cfg core.Config) bool {
	changed := !reflect.DeepEqual(m.cfg.Tools, cfg.Tools)
	for _, old := range m.cfg.Hosts {
		next, exists := cfg.Host(old.ID)
		if !exists || !reflect.DeepEqual(old, next) {
			changed = true
			break
		}
	}
	if changed {
		if len(m.startup.Queue) > 0 {
			m.startup.Lines = append(m.startup.Lines, "Cancelled queued startup tools because their configuration changed.")
			m.startup.Queue = nil
			m.startup.FocusAllowed = false
		}
		for id := range m.probeGeneration {
			if cancel := m.probeCancel[id]; cancel != nil {
				cancel()
			}
			m.probeGeneration[id]++
			m.probing[id] = false
		}
		m.availability = map[string]map[string]core.Availability{}
		m.cachedAt = map[string]time.Time{}
		m.probeErrors = map[string]string{}
	}
	m.cfg = cfg
	m.observe()
	if _, ok := cfg.Host(m.hostID); !ok {
		m.hostID = cfg.DefaultHost
		m.active = ""
	}
	if _, ok := cfg.Set(m.setID); !ok {
		m.setID = cfg.DefaultSet
	}
	if m.setID == catalog.AllSetID {
		m.setID = m.browseSet()
		m.explore = true
	}
	m.resetSelection()
	return changed
}
