package tui

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazyset/internal/config"
	"github.com/daviddwlee84/lazyset/internal/core"
)

type action struct{ ID, Label, Key string }

var actions = []action{
	{"hosts", "Choose host", "H"}, {"sets", "Choose / manage set", "S"},
	{"explore", "All tools (Explore / back to set)", "e"}, {"refresh", "Refresh availability", "r"},
	{"toolinfo", "Tool details / installation", "i"},
	{"visibility", "Filter tool states", "f"}, {"startup", "Startup results and warnings", ""},
	{"connect", "Connect / authenticate SSH", "c"}, {"host-add", "Add SSH host", ""}, {"sessions", "Open sessions", "s"},
	{"close", "Close current session", "x"},
	{"restart", "Restart current tool", ""}, {"reopen", "Reopen exited session", ""}, {"external", "Open selected tool externally", ""}, {"stop", "Stop current tool", ""},
	{"stop-all", "Stop all sessions", ""}, {"config", "Edit configuration", ""},
	{"hosts-config", "Edit hosts configuration", ""},
	{"mouse", "Toggle mouse capture", ""}, {"help", "Keyboard help", "?"},
	{"quit", "Quit lazyset", ""},
}
var prefixActions = []action{
	{"observe", "Return to observation", "esc"}, {"next", "Next embedded tool", "n"},
	{"previous", "Previous embedded tool", "p"}, {"palette", "Command menu", "/"},
	{"help", "Keyboard help", "?"}, {"send-q", "Send literal q", "q"},
	{"hosts", "Choose host", "h"}, {"sets", "Choose set", "s"}, {"quit", "Quit lazyset", "Q"},
	{"palette", "Commands (:q, :close, :sessions)", ":"}, {"close", "Close current session", "x"},
	{"send-next", "Send next key directly to tool", "v"},
}

func commandAliases(id string) []string {
	switch id {
	case "quit":
		return []string{"q", "quit"}
	case "explore":
		return []string{"all", "explore"}
	case "toolinfo":
		return []string{"info", "toolinfo"}
	default:
		return []string{id}
	}
}

func (m *Model) key(k tea.KeyPressMsg) tea.Cmd {
	key := k.String()
	if k.IsRepeat && key == m.returnGuardKey {
		return nil
	}
	if !k.IsRepeat {
		m.returnGuardKey = ""
	}
	if m.overlay != "" {
		return m.overlayKey(k)
	}
	if m.filtering {
		switch key {
		case "esc":
			m.filter = ""
			m.filtering = false
			m.input.Blur()
			m.resetSelection()
			return nil
		case "enter":
			m.filtering = false
			m.input.Blur()
			return nil
		case "up":
			m.move(-1)
			return nil
		case "down":
			m.move(1)
			return nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(k)
		m.syncFilter()
		return cmd
	}
	if m.literalNext {
		target := m.literalTarget
		m.literalNext = false
		m.literalTarget = ""
		if target == m.active {
			m.send(k)
		}
		return nil
	}
	if m.prefix {
		m.prefix = false
		if prefixMatch(k, m.cfg.Prefix) {
			m.literal(prefixByte(m.cfg.Prefix))
			return nil
		}
		for _, a := range prefixActions {
			if a.Key == key {
				return m.action(a.ID)
			}
		}
		m.status = "Unknown prefix command. " + prefixLabel(m.cfg.Prefix) + " ? shows help."
		return nil
	}
	if prefixMatch(k, m.cfg.Prefix) && !k.IsRepeat {
		m.prefix = true
		return nil
	}
	if m.interact {
		if r := m.current(); r != nil && matchesReturnKey(k, effectiveReturnKeys(r.Tool)) {
			m.observe()
			m.returnGuardKey = key
			m.status = "Observing; tool is still running."
			return nil
		}
		m.send(k)
		return nil
	}
	if k.IsRepeat && (key == "enter" || key == "q" || key == " " || key == "space") {
		return nil
	}
	if key != "g" {
		m.pendingG = false
	}
	switch key {
	case "enter":
		if m.focus > 0 {
			return m.action(m.buttons()[m.focus-1].ID)
		}
		if r := m.current(); r != nil && r.Tool.ID == m.selected && r.Term != nil && !ended(r.Term) {
			m.interact = true
			m.status = "Input goes to " + r.Host.Name + " / " + r.Tool.Name
			return nil
		}
		if r := m.current(); r != nil && r.Tool.ID == m.selected && !r.Starting && (r.Term == nil || ended(r.Term)) {
			return m.action("reopen")
		}
		return m.activate(m.selected, false)
	case "up", "k":
		m.focus = 0
		m.move(-1)
	case "down", "j":
		m.focus = 0
		m.move(1)
	case "home":
		m.focus = 0
		m.move(-len(m.toolIDs()))
	case "end", "G":
		m.focus = 0
		m.move(len(m.toolIDs()))
	case "g":
		if m.pendingG {
			m.move(-len(m.toolIDs()))
			m.pendingG = false
		} else {
			m.pendingG = true
		}
	case "pgup":
		m.move(-max(1, m.layout().th))
	case "pgdown":
		m.move(max(1, m.layout().th))
	case "tab", "right", "l":
		m.focus = (m.focus + 1) % (len(m.buttons()) + 1)
	case "shift+tab", "left", "h":
		m.focus = (m.focus + len(m.buttons())) % (len(m.buttons()) + 1)
	case "/":
		m.filtering = true
		m.input.Prompt = "Filter: "
		m.input.Placeholder = ""
		m.input.SetValue(m.filter)
		m.input.CursorEnd()
		return m.input.Focus()
	case " ", "space", ":":
		return m.action("palette")
	case "esc", "q":
		m.focus = 0 // Never turn a child's repeated q into host exit.
	case "ctrl+c":
		return m.action("quit")
	default:
		for _, a := range actions {
			if a.Key != "" && a.Key == key {
				return m.action(a.ID)
			}
		}
	}
	return nil
}

func prefixByte(prefix string) []byte {
	s := strings.TrimPrefix(prefix, "ctrl+")
	if s == "space" {
		return []byte{0}
	}
	r := []rune(s)
	if len(r) == 1 {
		return []byte{byte(unicode.ToUpper(r[0])) & 31}
	}
	return []byte{28}
}
func prefixMatch(k tea.KeyPressMsg, prefix string) bool {
	if k.Keystroke() == prefix {
		return true
	}
	aliases := map[string]string{"ctrl+i": "tab", "ctrl+m": "enter", "ctrl+[": "esc", "ctrl+j": "ctrl+j", "ctrl+space": "ctrl+@"}
	return aliases[prefix] != "" && k.String() == aliases[prefix]
}
func prefixLabel(p string) string { return strings.ReplaceAll(p, "ctrl+", "Ctrl+") }

func matchesReturnKey(key tea.KeyPressMsg, bindings []string) bool {
	for _, binding := range bindings {
		if key.String() == binding || key.Keystroke() == binding || prefixMatch(key, binding) {
			return true
		}
	}
	return false
}

func (m *Model) action(id string) tea.Cmd {
	if m.busy && id != "help" && id != "observe" {
		m.status = "An operation is already in progress."
		return nil
	}
	switch id {
	case "focus-tool":
		m.prefix = false
		if m.interact {
			m.observe()
		} else if r := m.current(); r != nil && r.Term != nil && !ended(r.Term) {
			m.interact = true
			m.focus = 0
		}
	case "observe":
		m.observe()
	case "next":
		return m.cycle(1)
	case "previous":
		return m.cycle(-1)
	case "send-q":
		m.literal([]byte("q"))
	case "send-next":
		if r := m.current(); r != nil && r.Term != nil && !ended(r.Term) {
			m.literalNext = true
			m.literalTarget = m.active
			m.status = "The next key will go directly to " + r.Host.Name + " / " + r.Tool.Name
		}
	case "hosts", "sets", "sessions", "help", "toolinfo", "startup":
		m.observe()
		m.overlay = id
		m.menuIndex = 0
		selected := ""
		switch id {
		case "hosts":
			selected = m.hostID
		case "sets":
			selected = m.setID
			if m.explore {
				selected = "all"
			}
		case "sessions":
			selected = m.active
		}
		for i, row := range m.menuRows() {
			if row.ID == selected {
				m.menuIndex = i
				break
			}
		}
	case "palette":
		m.observe()
		m.overlay = "palette"
		m.menuIndex = 0
		m.input.SetValue("")
		m.input.Prompt = ": "
		m.input.Placeholder = "q, close, sessions, config…"
		return m.input.Focus()
	case "visibility":
		m.observe()
		m.visibilityDraft = m.visibility
		m.overlay = "visibility"
		m.menuIndex = 0
	case "host-add":
		return m.beginHost()
	case "close":
		return m.requestClose(m.active, "")
	case "explore":
		m.switchContext(m.hostID, m.setID, !m.explore)
	case "refresh":
		return m.refresh()
	case "connect":
		h, _ := m.cfg.Host(m.hostID)
		if h.SSH == "" {
			m.status = "Local host needs no SSH connection."
			return nil
		}
		// SSH config inspection may perform I/O: prepare the handoff in an effect.
		m.observe()
		m.busy = true
		svc := m.service
		return func() tea.Msg {
			cmd, err := svc.ConnectCommand(context.Background(), h)
			return connectReadyMsg{Host: h.ID, Command: cmd, Err: err}
		}
	case "config", "hosts-config":
		m.observe()
		m.busy = true
		path := m.path
		hosts := id == "hosts-config"
		if hosts {
			path = m.sources.HostsPath
		}
		return func() tea.Msg {
			if hosts {
				cmd, err := config.EditHostsCommand(path)
				return editorReadyMsg{Command: cmd, Err: err, Hosts: true}
			}
			cmd, err := config.EditCommand(path)
			return editorReadyMsg{Command: cmd, Err: err}
		}
	case "external":
		return m.openExternally()
	case "reopen":
		r := m.current()
		if r == nil || r.Starting || r.Term != nil && !ended(r.Term) {
			m.status = "Choose an exited session to reopen."
			return nil
		}
		return m.activate(r.Tool.ID, true)
	case "mouse":
		m.cfg.Mouse = !m.cfg.Mouse
		m.status = fmt.Sprintf("Mouse capture: %t (temporary; config edit to persist)", m.cfg.Mouse)
	case "restart", "stop":
		r := m.current()
		if r == nil || r.Starting {
			m.status = "No ready session selected."
			return nil
		}
		if id == "restart" && (r.Term == nil || ended(r.Term)) {
			return m.activate(r.Tool.ID, true)
		}
		if r.Term == nil || ended(r.Term) {
			m.status = "This session has already exited."
			return nil
		}
		m.confirm(id, r.Key, []string{r.Host.Name + " / " + r.Tool.Name, "This ends the current process and its unsaved work."})
	case "stop-all":
		keys := m.liveKeys()
		if len(keys) == 0 && len(m.startup.Queue) == 0 {
			m.status = "No sessions are running."
			return nil
		}
		if len(m.startup.Queue) > 0 {
			keys = append(keys, fmt.Sprintf("%d queued startup tools", len(m.startup.Queue)))
		}
		m.confirm(id, "", keys)
	case "quit":
		keys := m.liveKeys()
		if len(keys) == 0 && len(m.startup.Queue) == 0 {
			m.cancelStartup()
			return tea.Quit
		}
		if len(m.startup.Queue) > 0 {
			keys = append(keys, fmt.Sprintf("%d queued startup tools", len(m.startup.Queue)))
		}
		m.confirm(id, "", append([]string{"Quit and end these sessions?"}, keys...))
	}
	return nil
}

func (m *Model) confirm(action, target string, lines []string) {
	m.observe()
	m.overlay = "confirm"
	m.confirmAction = action
	m.confirmTarget = target
	m.confirmLines = lines
	m.confirmReturn = ""
	m.confirmIndex = 0
	m.menuIndex = 0
}

func (m *Model) liveKeys() []string {
	var keys []string
	for key, r := range m.sessions {
		if r.Starting || (r.Term != nil && !ended(r.Term)) {
			keys = append(keys, key)
		}
	}
	sortStrings(keys)
	return keys
}
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func (m *Model) confirmApply() tea.Cmd {
	id, target := m.confirmAction, m.confirmTarget
	m.overlay = m.confirmReturn
	if id == "quit" {
		m.cancelStartup()
		return tea.Quit
	}
	if id == "delete-set" {
		sources, expected := m.sources, append([]byte(nil), m.draftSnapshot...)
		m.busy = true
		return func() tea.Msg {
			err := config.DeleteSetSourcesIfUnchanged(sources, target, expected)
			cfg, loadErr := config.LoadSources(sources)
			if err == nil {
				err = loadErr
			}
			return savedSetMsg{Config: cfg, Err: err}
		}
	}
	keys := []string{target}
	if id == "stop-all" {
		m.cancelStartup()
		keys = m.liveKeys()
	}
	var terms []terminal
	for _, key := range keys {
		if r := m.sessions[key]; r != nil {
			generation, owned := m.pool.cancel(key)
			r.Generation = generation
			if owned != nil {
				terms = append(terms, owned)
			}
			if r.Term != nil && r.Term != owned {
				terms = append(terms, r.Term)
			}
			if r.Starting {
				r.Starting = false
				r.Err = "Launch cancelled."
			}
		}
	}
	m.busy = true
	return func() tea.Msg {
		var err error
		for _, s := range terms {
			if e := s.Stop(); e != nil {
				err = e
			}
		}
		restart := ""
		if id == "restart" {
			restart = target
		}
		closeKey := ""
		if id == "close" {
			closeKey = target
		}
		return stoppedMsg{Keys: keys, Restart: restart, Err: err, Close: closeKey}
	}
}

func (m *Model) syncFilter() {
	if !m.filtering {
		return
	}
	value := m.input.Value()
	if value != m.filter {
		m.filter = value
		m.selected = ""
		m.scroll = 0
		m.resetSelection()
	}
}

func (m *Model) prompt(purpose, value string) tea.Cmd {
	m.overlay = "prompt"
	m.promptPurpose = purpose
	m.input.SetValue(value)
	m.input.Prompt = "> "
	m.input.Placeholder = ""
	m.input.CursorEnd()
	return m.input.Focus()
}

func (m *Model) overlayKey(k tea.KeyPressMsg) tea.Cmd {
	if m.busy {
		return nil
	}
	key := k.String()
	if m.overlay == "hostedit" {
		return m.hostKey(k)
	}
	if key == "esc" || key == "ctrl+c" {
		if m.busy {
			return nil
		}
		if m.overlay == "confirm" {
			m.cancelConfirm()
			return nil
		} else if m.overlay == "addtools" || m.overlay == "prompt" {
			m.overlay = "setedit"
		} else {
			m.overlay = ""
		}
		m.input.Blur()
		m.menuIndex = 0
		return nil
	}
	if m.overlay == "prompt" {
		if key == "enter" {
			name := strings.TrimSpace(m.input.Value())
			if name == "" {
				m.status = "Set name cannot be empty."
				return nil
			}
			m.draft.Name = name
			if m.draft.ID == "" {
				m.draft.ID = m.newSetID(name)
			}
			m.overlay = "setedit"
			m.input.Blur()
			m.menuIndex = 0
			return nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(k)
		return cmd
	}
	if m.overlay == "palette" && key != "up" && key != "down" && key != "enter" {
		old := m.input.Value()
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(k)
		if m.input.Value() != old {
			m.menuIndex = 0
		}
		return cmd
	}
	if m.overlay == "visibility" {
		switch key {
		case "space", " ":
			m.visibilityToggle(m.menuIndex)
		case "a":
			m.visibilityDraft = allVisible()
		case "o":
			m.visibilityOnly(m.menuIndex)
		case "up", "k":
			m.menuIndex = max(0, m.menuIndex-1)
		case "down", "j":
			m.menuIndex = min(3, m.menuIndex+1)
		case "enter":
			m.applyVisibility()
		}
		return nil
	}
	if m.overlay == "confirm" {
		if key == "left" || key == "right" || key == "tab" || key == "h" || key == "l" {
			m.menuIndex = 1 - m.menuIndex
		}
		if key == "enter" && !k.IsRepeat {
			if m.menuIndex == 1 {
				return m.confirmApply()
			}
			m.cancelConfirm()
		}
		return nil
	}
	if m.overlay == "help" || m.overlay == "toolinfo" || m.overlay == "startup" {
		if key == "q" || key == "enter" {
			m.overlay = ""
		}
		if key == "down" || key == "j" {
			m.menuIndex = min(max(0, len(m.documentLines())-1), m.menuIndex+1)
		}
		if key == "up" || key == "k" {
			m.menuIndex = max(0, m.menuIndex-1)
		}
		if key == "pgdown" {
			m.menuIndex = min(max(0, len(m.documentLines())-1), m.menuIndex+max(1, m.height-6))
		}
		if key == "pgup" {
			m.menuIndex = max(0, m.menuIndex-max(1, m.height-6))
		}
		return nil
	}
	if m.overlay == "sets" {
		if key == "n" {
			return m.beginSet(core.Set{}, true)
		}
		if len(m.cfg.Sets) > 0 && (key == "c" || key == "e" || key == "d") {
			s := m.cfg.Sets[min(m.menuIndex, len(m.cfg.Sets)-1)]
			if key == "d" {
				if s.Builtin {
					m.status = "Built-in sets stay available. Copy one to customize it."
					return nil
				}
				return m.prepareDelete(s)
			}
			copySet := key == "c" || s.Builtin
			if copySet {
				s.ID = ""
				s.Name = "My " + s.Name
				s.Builtin = false
			}
			return m.beginSet(s, copySet)
		}
	}
	if m.overlay == "hosts" && key == "n" {
		return m.beginHost()
	}
	if m.overlay == "hosts" && key == "e" {
		m.overlay = ""
		return m.action("hosts-config")
	}
	if m.overlay == "sessions" && key == "x" && !k.IsRepeat {
		rows := m.menuRows()
		if len(rows) > 0 {
			return m.requestClose(rows[min(m.menuIndex, len(rows)-1)].ID, "sessions")
		}
		return nil
	}
	if m.overlay == "setedit" {
		switch key {
		case "a":
			m.overlay = "addtools"
			m.menuIndex = 0
			return nil
		case "n":
			return m.prompt("set-name", m.draft.Name)
		case "ctrl+s":
			return m.saveDraft()
		case "x":
			if len(m.draft.Tools) > 0 {
				i := min(m.menuIndex, len(m.draft.Tools)-1)
				m.draft.Tools = append(m.draft.Tools[:i], m.draft.Tools[i+1:]...)
				m.menuIndex = max(0, min(i, len(m.draft.Tools)-1))
			}
			return nil
		case "u", "d":
			i := m.menuIndex
			j := i - 1
			if key == "d" {
				j = i + 1
			}
			if i >= 0 && i < len(m.draft.Tools) && j >= 0 && j < len(m.draft.Tools) {
				m.draft.Tools[i], m.draft.Tools[j] = m.draft.Tools[j], m.draft.Tools[i]
				m.menuIndex = j
			}
			return nil
		}
	}
	count := len(m.menuRows())
	switch key {
	case "up", "k":
		m.menuIndex = max(0, m.menuIndex-1)
	case "down", "j":
		m.menuIndex = min(max(0, count-1), m.menuIndex+1)
	case "home":
		m.menuIndex = 0
	case "end", "G":
		m.menuIndex = max(0, count-1)
	case "enter":
		if !k.IsRepeat {
			return m.menuAccept()
		}
	case "q":
		m.overlay = ""
	}
	return nil
}

func (m *Model) menuAccept() tea.Cmd {
	if m.busy {
		return nil
	}
	rows := m.menuRows()
	if len(rows) == 0 {
		return nil
	}
	m.menuIndex = min(m.menuIndex, len(rows)-1)
	row := rows[m.menuIndex]
	switch m.overlay {
	case "hosts":
		m.overlay = ""
		m.switchContext(row.ID, m.setID, m.explore)
		if m.availability[row.ID] == nil {
			return m.refresh()
		}
	case "sets":
		m.overlay = ""
		m.switchContext(m.hostID, row.ID, false)
	case "palette":
		m.overlay = ""
		m.input.Blur()
		return m.action(row.ID)
	case "sessions":
		if r := m.sessions[row.ID]; r != nil {
			m.overlay = ""
			m.switchContext(r.Host.ID, m.setID, true)
			m.active = r.Key
			m.selected = r.Tool.ID
			m.filter = ""
			m.clampScroll()
		}
	case "addtools":
		m.draft.Tools = append(m.draft.Tools, row.ID)
		m.overlay = "setedit"
		m.menuIndex = len(m.draft.Tools) - 1
	case "visibility":
		m.visibilityToggle(m.menuIndex)
	}
	return nil
}

func (m *Model) beginSet(s core.Set, rename bool) tea.Cmd {
	m.observe()
	m.busy = true
	path := m.path
	s.Tools = append([]string(nil), s.Tools...)
	return func() tea.Msg { b, err := config.Snapshot(path); return editSetMsg{s, b, err, rename} }
}

func (m *Model) prepareDelete(s core.Set) tea.Cmd {
	m.busy = true
	path := m.path
	return func() tea.Msg { b, err := config.Snapshot(path); return deleteReadyMsg{Set: s, Snapshot: b, Err: err} }
}

func (m *Model) saveDraft() tea.Cmd {
	if m.busy {
		return nil
	}
	m.busy = true
	sources := m.sources
	draft := m.draft
	draft.Tools = append([]string(nil), draft.Tools...)
	expected := m.draftSnapshot
	return func() tea.Msg {
		err := config.SaveSetSourcesIfUnchanged(sources, draft, expected)
		cfg, loadErr := config.LoadSources(sources)
		if err == nil {
			err = loadErr
		}
		return savedSetMsg{draft.ID, cfg, err}
	}
}

func (m *Model) newSetID(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	id := strings.Trim(b.String(), "-")
	if id == "" {
		id = "custom"
	}
	base := id
	for n := 2; ; n++ {
		if _, ok := m.cfg.Set(id); !ok {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
}
