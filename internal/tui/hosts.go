package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"lazyset/internal/config"
	"lazyset/internal/core"
)

type hostDraftMsg struct {
	Snapshot []byte
	Err      error
}
type hostSavedMsg struct {
	ID     string
	Config core.Config
	Err    error
}

func (m *Model) beginHost() tea.Cmd {
	m.observe()
	m.busy = true
	path := m.sources.HostsPath
	return func() tea.Msg { snapshot, err := config.Snapshot(path); return hostDraftMsg{snapshot, err} }
}
func (m *Model) initHostForm(snapshot []byte) tea.Cmd {
	m.hostSnapshot = snapshot
	m.hostField = 0
	for i := range m.hostInputs {
		input := textinput.New()
		input.Prompt = "> "
		input.CharLimit = 256
		input.SetWidth(max(1, m.width-6))
		m.hostInputs[i] = input
	}
	m.hostInputs[0].Placeholder = "SSH alias or user@host"
	m.hostInputs[1].Placeholder = "Optional display name"
	m.overlay = "hostedit"
	m.status = "Save adds configuration only. Connect handles authentication later."
	return m.hostInputs[0].Focus()
}
func (m *Model) hostKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc", "ctrl+c":
		m.hostInputs[m.hostField].Blur()
		m.overlay = "hosts"
		m.menuIndex = 0
		return nil
	case "ctrl+s":
		return m.saveHost()
	case "enter":
		if k.IsRepeat {
			return nil
		}
		if m.hostField == 1 {
			return m.saveHost()
		}
		return m.focusHostField(1)
	case "tab", "shift+tab", "up", "down":
		return m.focusHostField(1 - m.hostField)
	}
	var cmd tea.Cmd
	m.hostInputs[m.hostField], cmd = m.hostInputs[m.hostField].Update(k)
	return cmd
}
func (m *Model) focusHostField(index int) tea.Cmd {
	m.hostInputs[m.hostField].Blur()
	m.hostField = index
	return m.hostInputs[index].Focus()
}
func (m *Model) saveHost() tea.Cmd {
	if m.busy {
		return nil
	}
	alias := strings.TrimSpace(m.hostInputs[0].Value())
	name := strings.TrimSpace(m.hostInputs[1].Value())
	if alias == "" {
		m.status = "Enter an SSH alias or user@host."
		return m.focusHostField(0)
	}
	if name == "" {
		name = alias
	}
	id := identifier(alias)
	base := id
	for n := 2; ; n++ {
		if _, exists := m.cfg.Host(id); !exists {
			break
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
	h := core.Host{ID: id, Name: name, SSH: alias}
	cfg := m.cfg
	cfg.Hosts = append(append([]core.Host(nil), cfg.Hosts...), h)
	if err := config.Validate(cfg); err != nil {
		m.status = err.Error()
		return nil
	}
	m.busy = true
	sources, expected := m.sources, m.hostSnapshot
	return func() tea.Msg {
		if err := config.SaveHostSourcesIfUnchanged(sources, h, expected); err != nil {
			return hostSavedMsg{Err: err}
		}
		updated, err := config.LoadSources(sources)
		return hostSavedMsg{h.ID, updated, err}
	}
}
func identifier(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	id := strings.Trim(b.String(), "-")
	if id == "" {
		id = "host"
	}
	return id
}
