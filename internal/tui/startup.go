package tui

import (
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazyset/internal/core"
)

const startupConcurrency = 3

type startupBatch struct {
	HostID                           string
	Queue                            []string
	Inflight                         map[string]uint64
	Lines                            []string
	Diagnostics                      []string
	FocusAllowed, Focused, Cancelled bool
	WaitReason                       string
}

// ResolveStartup validates names without touching executables or hosts. Explicit
// tools precede set members; each tool is launched at most once per invocation.
func ResolveStartup(cfg core.Config, opts Options) ([]string, []string) {
	var ids, warnings []string
	seen := map[string]bool{}
	add := func(id string) {
		id = strings.TrimSpace(id)
		if seen[id] {
			return
		}
		seen[id] = true
		if _, ok := cfg.Tool(id); !ok {
			warnings = append(warnings, fmt.Sprintf("Skipped unknown tool %q.", id))
			return
		}
		ids = append(ids, id)
	}
	if opts.Tool != "" {
		add(opts.Tool)
	}
	for _, id := range opts.Tools {
		add(id)
	}
	seenSets := map[string]bool{}
	for _, id := range opts.StartSets {
		id = strings.TrimSpace(id)
		if seenSets[id] {
			continue
		}
		seenSets[id] = true
		s, ok := cfg.Set(id)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("Skipped unknown set %q.", id))
			continue
		}
		for _, tool := range s.Tools {
			add(tool)
		}
	}
	return ids, warnings
}

func (m *Model) pumpStartup() tea.Cmd {
	b := &m.startup
	if b.Cancelled || len(b.Queue) == 0 {
		return nil
	}
	h, ok := m.cfg.Host(b.HostID)
	if !ok {
		b.warn("Startup cancelled: the requested host no longer exists.")
		b.Queue = nil
		return nil
	}
	if m.probing[h.ID] || m.availability[h.ID] == nil {
		return nil
	}
	if reason := m.probeErrors[h.ID]; reason != "" || !m.cachedAt[h.ID].IsZero() {
		if reason == "" {
			reason = "availability is cached"
		}
		if b.WaitReason != reason {
			b.WaitReason = reason
			b.warn("Startup waiting for fresh discovery on " + h.ID + ": " + reason + ". Use Connect or Refresh.")
		}
		return nil
	}
	b.WaitReason = ""
	var cmds []tea.Cmd
	for len(b.Queue) > 0 && len(b.Inflight) < startupConcurrency {
		id := b.Queue[0]
		b.Queue = b.Queue[1:]
		t, exists := m.cfg.Tool(id)
		if !exists {
			b.warn("Skipped removed tool " + id + ".")
			continue
		}
		if t.Mode != "embedded" {
			b.warn("Skipped " + id + ": external tools require a manual launch.")
			continue
		}
		a := m.availability[h.ID][id]
		if a.State != "found" {
			state := a.State
			if state == "" {
				state = "unknown"
			}
			b.warn("Skipped " + id + ": " + state + ". " + a.Reason)
			continue
		}
		key := sessionKey(h.ID, id)
		if _, exists := m.sessions[key]; exists {
			b.Lines = append(b.Lines, "Already open: "+key)
			continue
		}
		focus := b.FocusAllowed && !b.Focused && m.hostID == h.ID && m.overlay == ""
		if focus {
			b.Focused = true
			m.filter = ""
			m.visibility = allVisible()
			if !contains(m.toolIDs(), id) {
				m.explore = true
			}
		}
		cmd := m.launchEmbedded(h, t, a.Path, focus)
		b.Inflight[key] = m.sessions[key].Generation
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

func (m *Model) completeStartup(msg startedMsg) {
	if generation, ok := m.startup.Inflight[msg.Key]; !ok || generation != msg.Generation {
		return
	}
	delete(m.startup.Inflight, msg.Key)
	r := m.sessions[msg.Key]
	if r == nil || r.Generation != msg.Generation || m.startup.Cancelled {
		m.startup.Lines = append(m.startup.Lines, "Cancelled: "+msg.Key)
		return
	}
	if msg.Err != nil {
		m.startup.warn("Failed to start " + msg.Key + ": " + msg.Err.Error())
	} else {
		m.startup.Lines = append(m.startup.Lines, "Started: "+msg.Key)
	}
}

func (m *Model) cancelStartup() {
	if len(m.startup.Queue) > 0 {
		m.startup.Lines = append(m.startup.Lines, fmt.Sprintf("Cancelled %d queued startup tools.", len(m.startup.Queue)))
	}
	m.startup.Queue = nil
	m.startup.Cancelled = true
	m.startup.FocusAllowed = false
}

func (m *Model) startupLines() []string {
	lines := []string{fmt.Sprintf("Target: %s · %d queued · %d starting", clean(m.startup.HostID), len(m.startup.Queue), len(m.startup.Inflight)), ""}
	if len(m.startup.Lines) == 0 {
		lines = append(lines, "No startup requests or warnings.")
	}
	for _, s := range m.startup.Lines {
		lines = append(lines, clean(s))
	}
	return lines
}

func (b *startupBatch) warn(message string) {
	b.Lines = append(b.Lines, message)
	b.Diagnostics = append(b.Diagnostics, message)
}

// Runtime warnings are emitted only after Bubble Tea restores the terminal;
// writing stderr while the alternate screen owns it would corrupt the view.
func (m *Model) writeStartupWarnings(out io.Writer) error {
	for _, message := range m.startup.Diagnostics {
		if _, err := fmt.Fprintln(out, "Warning:", clean(message)); err != nil {
			return err
		}
	}
	return nil
}

func (m *Model) launchEmbedded(h core.Host, t core.Tool, path string, focus bool) tea.Cmd {
	key := sessionKey(h.ID, t.ID)
	if focus {
		m.observe()
		m.active = key
		m.selected = t.ID
		m.focus = 0
		m.clampScroll()
	}
	generation := m.pool.reserve(key)
	m.sessions[key] = &running{Key: key, Host: h, Tool: t, Starting: true, Generation: generation}
	l, p, svc := m.layout(), m.pool, m.service
	return func() tea.Msg {
		spec, err := svc.LaunchSpec(h, t, path)
		if err != nil {
			return startedMsg{Key: key, Err: err, Generation: generation}
		}
		spec.Width = l.tw
		spec.Height = l.th
		s, err := p.start(key, generation, spec)
		return startedMsg{Key: key, Term: s, Err: err, Generation: generation}
	}
}
