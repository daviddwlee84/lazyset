package tui

import (
	"strings"

	"lazyset/internal/core"
)

type visibilityFilter struct{ Running, Available, Missing, Other bool }

func allVisible() visibilityFilter { return visibilityFilter{true, true, true, true} }
func (v visibilityFilter) shows(status string) bool {
	switch status {
	case "running":
		return v.Running
	case "available":
		return v.Available
	case "missing":
		return v.Missing
	default:
		return v.Other
	}
}
func (m *Model) toolStatus(id string) string {
	if r := m.sessions[sessionKey(m.hostID, id)]; r != nil && (r.Starting || r.Term != nil && !ended(r.Term)) {
		return "running"
	}
	if !m.cachedAt[m.hostID].IsZero() || m.probeErrors[m.hostID] != "" {
		return "other"
	}
	switch m.available(id).State {
	case "found":
		return "available"
	case "missing":
		return "missing"
	default:
		return "other"
	}
}
func (m *Model) visibilityLabel() string {
	if m.visibility == allVisible() {
		return "All states"
	}
	var names []string
	if m.visibility.Running {
		names = append(names, "Running")
	}
	if m.visibility.Available {
		names = append(names, "Available")
	}
	if m.visibility.Missing {
		names = append(names, "Not installed")
	}
	if m.visibility.Other {
		names = append(names, "Other")
	}
	if len(names) == 0 {
		return "All hidden"
	}
	return strings.Join(names, " + ")
}
func (m *Model) visibilityRows() []menuRow {
	labels := []menuRow{{"running", "Running", "Starting or running sessions"}, {"available", "Available", "Found executable, not running"}, {"missing", "Not installed", "Confirmed missing executable"}, {"other", "Other", "Unchecked, stale, offline or unsupported"}}
	for i := range labels {
		mark := "[ ] "
		if m.visibilityDraft.shows(labels[i].ID) {
			mark = "[x] "
		}
		labels[i].Label = mark + labels[i].Label
	}
	return labels
}
func (m *Model) visibilityToggle(index int) {
	switch index {
	case 0:
		m.visibilityDraft.Running = !m.visibilityDraft.Running
	case 1:
		m.visibilityDraft.Available = !m.visibilityDraft.Available
	case 2:
		m.visibilityDraft.Missing = !m.visibilityDraft.Missing
	case 3:
		m.visibilityDraft.Other = !m.visibilityDraft.Other
	}
}
func (m *Model) visibilityOnly(index int) {
	m.visibilityDraft = visibilityFilter{}
	m.visibilityToggle(index)
}
func (m *Model) applyVisibility() {
	m.visibility = m.visibilityDraft
	m.overlay = ""
	m.resetSelection()
	m.status = "Showing: " + m.visibilityLabel()
}

func effectiveReturnKeys(t core.Tool) []string {
	if t.ReturnKeys != nil {
		return t.ReturnKeys
	}
	if t.QToObserve {
		return []string{"q"}
	}
	return nil
}
