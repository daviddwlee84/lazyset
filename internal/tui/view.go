package tui

import (
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type paneRect struct{ x, y, w, h int }

func (r paneRect) contains(x, y int) bool {
	return r.w > 0 && r.h > 0 && x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

type geometry struct {
	sidebar, tx, ty, tw, th int
	list, terminal          paneRect
}

func (m *Model) layout() geometry {
	side := 0
	if m.width >= 100 {
		side = 29
	}
	x := 0
	if side > 0 {
		x = side + 1
	}
	tw, th := max(1, m.width-x-2), max(1, m.height-8)
	return geometry{
		sidebar: side, tx: x + 1, ty: 4, tw: tw, th: th,
		list:     paneRect{0, 3, side, th + 2},
		terminal: paneRect{x, 3, tw + 2, th + 2},
	}
}

type button struct {
	ID, Label string
	X, Y      int
}

func (b button) rect() paneRect {
	return paneRect{b.X, b.Y, ansi.StringWidth(b.Label) + 2, 1}
}

func (m *Model) buttons() []button {
	mode := "Interact"
	if m.interact {
		mode = "Back"
	}
	focusID := "focus-tool"
	if r := m.current(); r != nil && !r.Starting && (r.Term == nil || ended(r.Term)) {
		focusID, mode = "reopen", "Reopen"
	}
	out := []button{{ID: "hosts", Label: "Hosts"}, {ID: "sets", Label: "Sets"}, {ID: "explore", Label: "All"}, {ID: "visibility", Label: "Status"}, {ID: "sessions", Label: fmt.Sprintf("Sessions %d", len(m.sessions))}, {ID: "palette", Label: "Commands"}, {ID: "close", Label: "Close"}, {ID: focusID, Label: mode}}
	x := 0
	for i := range out {
		out[i].X = x
		out[i].Y = 1
		x += len(out[i].Label) + 3
	}
	out = append(out, button{ID: "quit", Label: "Quit", X: m.width - 6, Y: 0})
	return out
}
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, ansi.Strip(s))
}
func fit(s string, w int) string {
	w = max(0, w)
	s = ansi.Truncate(s, w, "")
	return s + strings.Repeat(" ", max(0, w-ansi.StringWidth(s)))
}
func (m *Model) style(s string, kind string) string {
	if m.noColor {
		return s
	}
	st := lipgloss.NewStyle()
	switch kind {
	case "title":
		st = st.Bold(true).Foreground(lipgloss.Color("81"))
	case "selected":
		st = st.Bold(true).Foreground(lipgloss.Color("16")).Background(lipgloss.Color("117"))
	case "muted":
		st = st.Foreground(lipgloss.Color("244"))
	case "warning":
		st = st.Foreground(lipgloss.Color("214"))
	case "live":
		st = st.Foreground(lipgloss.Color("120"))
	case "focus":
		st = st.Bold(true).Foreground(lipgloss.Color("81"))
	}
	return st.Render(s)
}

func (m *Model) View() tea.View {
	var content string
	if m.overlay == "confirm" {
		content = m.confirmView()
	} else if m.overlay != "" {
		content = m.overlayView()
	} else {
		content = m.mainView()
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.KeyboardEnhancements.ReportEventTypes = true
	if m.cfg.Mouse {
		v.MouseMode = tea.MouseModeCellMotion
	}
	if m.overlay == "hostedit" {
		if c := m.hostInputs[m.hostField].Cursor(); c != nil {
			copy := *c
			copy.X += 2
			copy.Y += 4
			if m.height > 10 {
				copy.Y += 3 * m.hostField
			}
			v.Cursor = &copy
		}
	} else if m.overlay == "prompt" || m.overlay == "palette" {
		if c := m.input.Cursor(); c != nil {
			copy := *c
			copy.X += 2
			copy.Y += 3
			v.Cursor = &copy
		}
	} else if m.overlay == "" && m.filtering {
		if c := m.input.Cursor(); c != nil {
			copy := *c
			copy.Y += max(0, m.height-3)
			v.Cursor = &copy
		}
	} else if m.overlay == "" && m.interact {
		if r := m.current(); r != nil && r.Frame.Cursor != nil {
			l := m.layout()
			c := *r.Frame.Cursor
			if c.X >= 0 && c.Y >= 0 && c.X < l.tw && c.Y < l.th {
				c.X += l.tx
				c.Y += l.ty
				v.Cursor = &c
			}
		}
	}
	if v.Cursor != nil && (v.Cursor.X < 0 || v.Cursor.Y < 0 || v.Cursor.X >= m.width || v.Cursor.Y >= m.height) {
		v.Cursor = nil
	}
	return v
}

func (m *Model) mainView() string {
	w, h := m.width, m.height
	if h < 9 || w < 24 {
		return m.smallView()
	}
	l := m.layout()
	lines := make([]string, h)
	for i := range lines {
		lines[i] = strings.Repeat(" ", w)
	}
	hostName := m.hostID
	if host, ok := m.cfg.Host(m.hostID); ok {
		hostName = host.Name
	}
	setName := m.setID
	if s, ok := m.cfg.Set(m.setID); ok {
		setName = s.Name
	}
	if m.explore {
		setName = "All"
	}
	mode := "OBSERVE"
	if m.interact {
		mode = "INTERACT"
	}
	title := m.style("lazyset", "title") + "  Host: " + clean(hostName) + "  Set: " + clean(setName) + "  [" + mode + "]"
	lines[0] = fit(title, w-7) + strings.Repeat(" ", 7)
	for i, b := range m.buttons() {
		label := "[" + b.Label + "]"
		if b.Y == 1 {
			label += " "
		}
		if m.focus == i+1 {
			label = m.style(label, "selected")
		}
		putCells(lines, b.X, b.Y, label)
	}
	contextLine := m.visibilityLabel() + " · f status filters"
	if len(m.startup.Lines) > 0 || len(m.startup.Queue) > 0 || len(m.startup.Inflight) > 0 {
		contextLine = ":startup results · " + contextLine
	}
	lines[2] = m.style(contextLine, "muted")
	caption := "Tool details"
	if r := m.current(); r != nil {
		caption = clean(r.Host.Name + " / " + r.Tool.Name)
		if r.Starting {
			caption += " · starting"
		} else if r.Frame.Exited || r.Term == nil && r.Err != "" {
			caption += " · exited"
		}
	}
	if m.interact {
		caption = "INPUT · " + caption
	} else {
		caption = "OBSERVE · " + caption
	}
	m.drawFrame(lines, l.terminal, caption, m.interact)
	if l.sidebar > 0 {
		m.drawFrame(lines, l.list, "TOOLS · / filter", !m.interact && m.focus == 0)
	}
	var terminalLines []string
	if r := m.current(); r != nil {
		switch {
		case r.Starting:
			terminalLines = []string{"Starting " + clean(r.Tool.Name) + "…"}
		case r.Err != "":
			terminalLines = strings.Split(ansi.Wrap("Could not start: "+clean(r.Err)+"\n\n[Reopen] / :reopen to try again · x to close this session.", l.tw, ""), "\n")
		case r.Frame.Exited:
			state := "exited"
			if r.Host.SSH != "" && r.Frame.ExitCode == 255 {
				state = "disconnected"
			}
			message := fmt.Sprintf("%s / %s %s (status %d).\n[Reopen] / :reopen to reopen · x to close this session.\n%s", clean(r.Host.Name), clean(r.Tool.Name), state, r.Frame.ExitCode, clean(r.Frame.Error))
			if r.Tool.ID == "dev" {
				message += "\nA dev handoff may intentionally end its dashboard. :external opens it with native terminal ownership."
			}
			terminalLines = strings.Split(ansi.Wrap(message+"\n\nLast terminal output:\n", l.tw, "")+r.Frame.Content, "\n")
		default:
			terminalLines = strings.Split(r.Frame.Content, "\n")
		}
	} else {
		terminalLines = m.detailLines(l.tw)
	}
	ids := m.toolIDs()
	for row := 0; row < l.th; row++ {
		if l.sidebar > 0 {
			left := ""
			i := m.scroll + row
			if i < len(ids) {
				id := ids[i]
				t, _ := m.cfg.Tool(id)
				mark := "·"
				a := m.available(id)
				switch a.State {
				case "found":
					mark = "+"
				case "missing":
					mark = "-"
				case "unsupported":
					mark = "×"
				}
				if r := m.sessions[sessionKey(m.hostID, id)]; r != nil && (r.Starting || r.Term != nil && !ended(r.Term)) {
					mark = "●"
				}
				prefix := "  "
				if id == m.selected {
					prefix = "> "
				}
				left = fit(prefix+mark+" "+clean(t.Name), l.list.w-2)
				if id == m.selected {
					left = m.style(left, "selected")
				} else if a.State != "found" {
					left = m.style(left, "muted")
				}
			}
			putCells(lines, l.list.x+1, l.ty+row, fit(left, l.list.w-2))
		}
		right := ""
		if row < len(terminalLines) {
			right = terminalLines[row]
		}
		putCells(lines, l.tx, l.ty+row, fit(right, l.tw))
	}
	if m.filtering {
		lines[h-3] = m.input.View()
	} else if t, ok := m.cfg.Tool(m.selected); ok {
		a := m.available(t.ID)
		lines[h-3] = clean(t.Name+" — "+t.Description) + " [" + a.State + "]"
		if m.filter != "" {
			lines[h-3] += "  filter: " + clean(m.filter)
		}
	}
	status := m.status
	if m.probing[m.hostID] {
		status = "Discovering tools… navigation remains available."
		if at := m.cachedAt[m.hostID]; !at.IsZero() {
			status = "Checking tools… cached availability from " + at.Format("15:04:05")
		}
	} else if e := m.probeErrors[m.hostID]; e != "" {
		status = "Discovery failed (previous results are stale): " + e
	}
	if m.busy {
		status = "Working… " + status
	}
	lines[h-2] = m.style(clean(status), "warning")
	lines[h-1] = m.footer(w)
	for i := range lines {
		lines[i] = fit(lines[i], w)
	}
	return strings.Join(lines, "\n")
}

// putCells composes cells while preserving the ANSI styles of both layers.
func putCells(lines []string, x, y int, text string) {
	if y < 0 || y >= len(lines) || x < 0 {
		return
	}
	width := ansi.StringWidth(lines[y])
	if x >= width {
		return
	}
	text = ansi.Truncate(text, width-x, "")
	end := x + ansi.StringWidth(text)
	lines[y] = ansi.Cut(lines[y], 0, x) + text + ansi.Cut(lines[y], end, width)
}

func (m *Model) drawFrame(lines []string, r paneRect, title string, focused bool) {
	if r.w < 2 || r.h < 2 {
		return
	}
	tl, tr, bl, br, horizontal, vertical := "╭", "╮", "╰", "╯", "─", "│"
	kind := "muted"
	if focused {
		tl, tr, bl, br, horizontal, vertical = "╔", "╗", "╚", "╝", "═", "║"
		kind = "focus"
	}
	label := ansi.Truncate(" "+clean(title)+" ", r.w-2, "")
	top := tl + label + strings.Repeat(horizontal, max(0, r.w-2-ansi.StringWidth(label))) + tr
	putCells(lines, r.x, r.y, m.style(top, kind))
	putCells(lines, r.x, r.y+r.h-1, m.style(bl+strings.Repeat(horizontal, r.w-2)+br, kind))
	for row := 1; row < r.h-1; row++ {
		putCells(lines, r.x, r.y+row, m.style(vertical, kind))
		putCells(lines, r.x+r.w-1, r.y+row, m.style(vertical, kind))
	}
}

func (m *Model) smallView() string {
	mode := "observe"
	if m.interact {
		mode = "interact"
	}
	lines := []string{"lazyset [" + mode + "]", "Terminal too small.", m.footer(m.width)}
	if m.overlay == "confirm" {
		lines = []string{"Confirm " + m.confirmAction, "Cancel / Confirm", "Tab then Enter"}
	}
	lines = lines[:min(len(lines), m.height)]
	for i := range lines {
		lines[i] = fit(lines[i], m.width)
	}
	return strings.Join(lines, "\n")
}

func (m *Model) detailLines(width int) []string {
	t, ok := m.cfg.Tool(m.selected)
	if !ok {
		return []string{"No matching tools.", "Explore all tools or clear the filter."}
	}
	a := m.available(t.ID)
	text := clean(t.Name) + "\n\n" + clean(t.Description) + "\n\nStatus: " + a.State
	if a.Path != "" {
		text += "\nExecutable: " + clean(a.Path)
	}
	if a.Reason != "" {
		text += "\n" + clean(a.Reason)
	}
	text += "\nCommand: " + clean(strings.Join(t.Command, " "))
	if t.Dir != "" {
		text += "\nDirectory: " + clean(t.Dir)
	} else {
		text += "\nDirectory: invocation directory (local) / home (SSH)"
	}
	if t.Hint != "" {
		text += "\n\n" + clean(t.Hint)
	}
	text += "\n\nMode: " + t.Mode
	if t.Mode == "external" {
		text += "\nEnter hands the terminal to this tool; exit it to return."
	} else {
		text += "\nEnter opens an observation view; Enter again or click the tool to interact."
		text += "\n" + m.returnSequence() + ": return to lazyset (Observe); the tool stays running."
		if keys := effectiveReturnKeys(t); len(keys) > 0 {
			text += "\nAdditional return keys: " + strings.Join(keys, ", ") + ".\nThese keys are intercepted in every child context, including text input.\n" + prefixLabel(m.cfg.Prefix) + " v sends the next key literally; " + prefixLabel(m.cfg.Prefix) + " q sends q."
		}
	}
	if len(t.QuitKeys) > 0 {
		text += "\nNative quit keys (verified defaults): " + strings.Join(t.QuitKeys, ", ")
	}
	if t.QuitHint != "" {
		text += "\n" + clean(t.QuitHint)
	}
	if t.InstallURL != "" {
		text += "\n\nOfficial installation / usage:\n" + clean(t.InstallURL)
	}
	return strings.Split(ansi.Wrap(text, max(1, width-1), ""), "\n")
}

type menuRow struct{ ID, Label, Detail string }

func (m *Model) menuRows() []menuRow {
	var rows []menuRow
	switch m.overlay {
	case "hosts":
		for _, h := range m.cfg.Hosts {
			kind := h.SSH
			if kind == "" {
				kind = "local"
			}
			rows = append(rows, menuRow{h.ID, h.Name, kind})
		}
	case "sets":
		for _, s := range m.cfg.Sets {
			kind := "custom"
			if s.Builtin {
				kind = "built-in"
			}
			rows = append(rows, menuRow{s.ID, s.Name, fmt.Sprintf("%d tools · %s", len(s.Tools), kind)})
		}
	case "palette":
		rows = m.commandRows()
	case "visibility":
		rows = m.visibilityRows()
	case "sessions":
		keys := make([]string, 0, len(m.sessions))
		for k := range m.sessions {
			keys = append(keys, k)
		}
		sortStrings(keys)
		for _, k := range keys {
			r := m.sessions[k]
			status := "running"
			if r.Starting {
				status = "starting"
			} else if r.Term == nil || ended(r.Term) {
				status = "exited"
			}
			rows = append(rows, menuRow{k, r.Host.Name + " / " + r.Tool.Name, status})
		}
	case "setedit":
		for _, id := range m.draft.Tools {
			t, _ := m.cfg.Tool(id)
			rows = append(rows, menuRow{id, t.Name, t.Description})
		}
	case "addtools":
		for _, t := range m.cfg.Tools {
			if !contains(m.draft.Tools, t.ID) {
				rows = append(rows, menuRow{t.ID, t.Name, t.Description})
			}
		}
	}
	return rows
}

func (m *Model) overlayView() string {
	if m.overlay == "confirm" {
		return m.confirmView()
	}
	w, h := m.width, m.height
	if h < 9 || w < 24 {
		return m.smallView()
	}
	lines := make([]string, h)
	title, help := m.overlay, "↑↓/jk select · Enter choose · Esc back"
	switch m.overlay {
	case "hosts":
		title = "Choose host"
		help = "Enter select · n add host · Esc back"
	case "sets":
		title = "Tool sets"
		help = "Enter use · n new · c copy · e edit · d delete · Esc back"
	case "sessions":
		title = "Sessions"
		help = "Enter view · x close selected session · Esc back"
	case "visibility":
		title = "Visible tool statuses"
		help = "↑↓ choose · Space toggle · Enter apply · Esc cancel"
	case "startup":
		title = "Startup results"
		help = "↑↓/jk scroll · Esc / q / Enter back"
	case "palette":
		title = "Commands"
		help = ":q quit · :close close tool · ↑↓ choose · Enter run · Esc cancel"
	case "hostedit":
		title = "Add SSH host"
		help = "Tab fields · Ctrl+S save · Esc cancel"
	case "setedit":
		title = "Edit set: " + m.draft.Name
		help = "a add · x remove · u/d reorder · n rename · Ctrl+S save · Esc cancel"
	case "addtools":
		title = "Add a tool to " + m.draft.Name
	case "prompt":
		title = "Set name"
		help = "Enter accept · Esc back (without saving)"
	case "help":
		title = "Keyboard help"
		help = "↑↓/jk scroll · Esc / q / Enter back"
	case "toolinfo":
		title = "Tool details / installation"
		help = "↑↓/jk scroll · Esc / q / Enter back"
	}
	lines[0] = m.style("lazyset · "+clean(title), "title")
	lines[1] = "Host: " + clean(m.hostID) + " · Prefix: " + prefixLabel(m.cfg.Prefix)
	start := 4
	if m.overlay == "hosts" {
		lines[2] = "  [Add host]  Save an SSH alias without connecting"
	}
	if m.overlay == "sessions" {
		lines[2] = "  [Close selected]  Ends only the chosen tool"
	}
	if m.overlay == "palette" {
		lines[2] = "  Type a command or choose below; Enter runs it."
	}
	if m.overlay == "palette" || m.overlay == "prompt" {
		lines[3] = "  " + m.input.View()
		start = 5
	}
	if m.overlay == "hostedit" {
		lines[3] = "  SSH alias / user@host"
		lines[4] = "  " + m.hostInputs[0].View()
		if h <= 10 && m.hostField == 1 {
			lines[3] = "  Display name (optional)"
			lines[4] = "  " + m.hostInputs[1].View()
		}
		if h > 10 {
			lines[6] = "  Display name (optional)"
			lines[7] = "  " + m.hostInputs[1].View()
		}
		if h > 13 {
			lines[10] = "  [Save host]  Writes " + clean(m.sources.HostsPath)
			lines[11] = "  Configure ports, keys and ProxyJump in ~/.ssh/config."
		}
	} else if m.overlay == "help" || m.overlay == "toolinfo" || m.overlay == "startup" {
		text := m.documentLines()
		for i := 0; i < h-5 && i+m.menuIndex < len(text); i++ {
			lines[i+3] = text[i+m.menuIndex]
		}
	} else if m.overlay != "prompt" {
		rows := m.menuRows()
		capacity := max(1, h-start-3)
		offset := max(0, m.menuIndex-capacity+1)
		for i := 0; i < capacity && offset+i < len(rows); i++ {
			r := rows[offset+i]
			p := "  "
			if offset+i == m.menuIndex {
				p = "> "
			}
			s := p + clean(r.Label)
			if r.Detail != "" {
				s += " — " + clean(r.Detail)
			}
			s = fit(s, w)
			if offset+i == m.menuIndex {
				s = m.style(s, "selected")
			}
			lines[start+i] = s
		}
		if len(rows) == 0 {
			lines[start] = "No items. Use the actions below or Esc to return."
		}
	}
	lines[h-2] = m.style(clean(m.status), "warning")
	lines[h-1] = help
	if m.busy {
		lines[h-2] = "Working…"
	}
	for i := range lines {
		lines[i] = fit(lines[i], w)
	}
	return strings.Join(lines, "\n")
}

func (m *Model) documentLines() []string {
	if m.overlay == "startup" {
		return m.startupLines()
	}
	if m.overlay == "toolinfo" {
		return m.detailLines(max(1, m.width-1))
	}
	text := []string{"Interact: " + m.returnSequence() + " returns to lazyset; the tool stays running.", "Observe: ↑↓/jk choose · Enter view, then Enter interact · Tab/←→ focus", "Switching returns to observation; ordinary q never quits the host.", ""}
	for _, a := range prefixActions {
		text = append(text, prefixLabel(m.cfg.Prefix)+" "+a.Key+"  "+a.Label)
	}
	text = append(text, prefixLabel(m.cfg.Prefix)+" twice  Send literal prefix", "", "Host shortcuts:")
	for _, a := range actions {
		if a.Key != "" {
			text = append(text, a.Key+"  "+a.Label)
		}
	}
	text = append(text, "", "Space / : opens Commands. :q quits; :close closes a tool; :sessions lists them.", "Top-right [Quit] exits lazyset; running or queued sessions require confirmation.", "f / :visibility filters Running, Available, and Not installed tools.", "Hosts → n adds an SSH alias; Ctrl+S saves without connecting.", "Built-in return keys protect only q where it is a native quit key; Esc, Ctrl+C, F10 and Q pass through by default.", "return_keys is per tool. It applies in all child contexts; prefix v sends the next key literally.", "Paste stays text. Click the live terminal to interact; click Back to observe.", "The focused pane has a double border. Close ends only the chosen session.", "External tools own the terminal and use their own exit keys.")
	return strings.Split(ansi.Wrap(strings.Join(text, "\n"), max(1, m.width-1), ""), "\n")
}
