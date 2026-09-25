package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

func (m *Model) commandRows() []menuRow {
	query := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(m.input.Value()), ":")))
	rowFor := func(a action) menuRow {
		detail := ":" + strings.Join(commandAliases(a.ID), " / :")
		if a.Key != "" {
			detail += " · " + a.Key
		}
		return menuRow{a.ID, a.Label, detail}
	}
	if query != "" {
		for _, a := range actions {
			for _, alias := range commandAliases(a.ID) {
				if query == alias {
					return []menuRow{rowFor(a)}
				}
			}
		}
	}
	var rows []menuRow
	for _, a := range actions {
		if strings.Contains(strings.ToLower(a.Label+" "+strings.Join(commandAliases(a.ID), " ")), query) {
			rows = append(rows, rowFor(a))
		}
	}
	return rows
}

func (m *Model) requestClose(key, returnTo string) tea.Cmd {
	if m.busy {
		return nil
	}
	r := m.sessions[key]
	if r == nil {
		m.status = "No session to close. Open a tool first, or choose one from Sessions."
		return nil
	}
	if !r.Starting && (r.Term == nil || ended(r.Term)) {
		m.forgetSession(key)
		m.overlay = returnTo
		m.status = "Session closed."
		return nil
	}
	index := m.menuIndex
	m.confirm("close", key, []string{r.Host.Name + " / " + r.Tool.Name, "Close this session and end its process?", "Other sessions will keep running."})
	m.confirmReturn = returnTo
	m.confirmIndex = index
	return nil
}

func (m *Model) forgetSession(key string) {
	m.pool.forget(key)
	delete(m.sessions, key)
	for k, v := range m.views {
		if v.Active == key {
			v.Active = ""
			m.views[k] = v
		}
	}
	if m.active == key {
		m.observe()
		m.active = ""
	}
	m.menuIndex = max(0, min(m.confirmIndex, len(m.menuRows())-1))
}

func (m *Model) cancelConfirm() {
	m.overlay = m.confirmReturn
	m.menuIndex = max(0, min(m.confirmIndex, len(m.menuRows())-1))
	m.pressed = ""
}

func (m *Model) openExternally() tea.Cmd {
	t, ok := m.cfg.Tool(m.selected)
	if !ok {
		m.status = "Select a tool to open externally."
		return nil
	}
	h, ok := m.cfg.Host(m.hostID)
	if !ok {
		return nil
	}
	if r := m.sessions[sessionKey(h.ID, t.ID)]; r != nil && (r.Starting || r.Term != nil && !ended(r.Term)) {
		m.status = "Stop or Close the embedded session before opening this tool externally."
		return nil
	}
	a := m.available(t.ID)
	if a.State != "found" || m.probing[h.ID] || m.probeErrors[h.ID] != "" || !m.cachedAt[h.ID].IsZero() {
		m.status = "Fresh executable discovery is required before an external launch."
		return nil
	}
	m.observe()
	m.busy = true
	t.Mode = "external"
	svc := m.service
	l := m.layout()
	return func() tea.Msg {
		spec, err := svc.LaunchSpec(h, t, a.Path)
		spec.Width = l.tw
		spec.Height = l.th
		return externalReadyMsg{Host: h, Tool: t, Spec: spec, Err: err}
	}
}
