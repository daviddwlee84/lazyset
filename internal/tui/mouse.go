package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

func (m *Model) mouse(msg tea.MouseMsg) tea.Cmd {
	if !m.cfg.Mouse || m.busy {
		return nil
	}
	p := msg.Mouse()
	if m.focusClickPending != "" {
		switch msg.(type) {
		case tea.MouseReleaseMsg:
			m.focusClickPending = ""
			return nil
		case tea.MouseMotionMsg:
			return nil
		}
	}
	if m.childMouse && m.interact && m.overlay == "" {
		_, release := msg.(tea.MouseReleaseMsg)
		_, motion := msg.(tea.MouseMotionMsg)
		if release || motion {
			l := m.layout()
			p.X = max(0, min(l.tw-1, p.X-l.tx))
			p.Y = max(0, min(l.th-1, p.Y-l.ty))
			if release {
				m.childMouse = false
				m.send(tea.MouseReleaseMsg(p))
			} else {
				m.send(tea.MouseMotionMsg(p))
			}
			return nil
		}
	}
	if wheel, ok := msg.(tea.MouseWheelMsg); ok {
		delta := 3
		if wheel.Button == tea.MouseWheelUp {
			delta = -3
		}
		if m.overlay != "" {
			if m.overlay == "hostedit" {
				return nil
			}
			if m.overlay == "help" || m.overlay == "toolinfo" || m.overlay == "startup" {
				m.menuIndex = max(0, min(max(0, len(m.documentLines())-1), m.menuIndex+delta))
				return nil
			}
			if m.overlay != "confirm" && m.overlay != "prompt" {
				m.menuIndex = max(0, min(max(0, len(m.menuRows())-1), m.menuIndex+delta))
			}
			return nil
		}
		if m.layout().list.contains(p.X, p.Y) {
			m.move(delta)
			return nil
		}
		if !m.interact {
			return nil
		}
	}
	target := m.hit(p.X, p.Y)
	if _, ok := msg.(tea.MouseClickMsg); ok && p.Button == tea.MouseLeft && target != "" {
		m.pressed = target
		return nil
	}
	if _, ok := msg.(tea.MouseReleaseMsg); ok && m.pressed != "" {
		pressed := m.pressed
		m.pressed = ""
		if pressed == target {
			return m.click(target)
		}
		return nil
	}
	if m.overlay != "" {
		return nil
	}
	l := m.layout()
	inside := (paneRect{l.tx, l.ty, l.tw, l.th}).contains(p.X, p.Y)
	if inside && !m.interact {
		if _, click := msg.(tea.MouseClickMsg); click && p.Button == tea.MouseLeft {
			if r := m.current(); r != nil && !r.Starting && !r.Frame.Exited && r.Err == "" && r.Term != nil && !ended(r.Term) {
				m.interact = true
				m.prefix, m.filtering = false, false
				m.input.Blur()
				m.focus, m.pressed = 0, ""
				m.status = "Input goes to " + r.Host.Name + " / " + r.Tool.Name
				if m.cfg.FocusClick == "focus-only" {
					m.focusClickPending = r.Key
					return nil
				}
			}
		}
	}
	if m.interact && inside {
		p.X -= l.tx
		p.Y -= l.ty
		switch msg.(type) {
		case tea.MouseClickMsg:
			m.childMouse = true
			m.send(tea.MouseClickMsg(p))
		case tea.MouseReleaseMsg:
			// A release without a captured child press must not leak after a
			// focus-only click, resize, overlay, or a cancelled host button.
			return nil
		case tea.MouseMotionMsg:
			// Captured drags are handled above. Do not leak an uncaptured
			// motion after focus-only input or a resized/cancelled gesture.
			return nil
		case tea.MouseWheelMsg:
			m.send(tea.MouseWheelMsg(p))
		}
	}
	return nil
}

// Hit testing and rendering share layout calculations; View never creates state.
func (m *Model) hit(x, y int) string {
	if x < 0 || y < 0 || x >= m.width || y >= m.height || m.width < 24 || m.height < 9 {
		return ""
	}
	if m.overlay != "" {
		if m.overlay == "confirm" {
			g := m.confirmationLayout()
			if g.cancel.contains(x, y) {
				return "cancel"
			}
			if g.accept.contains(x, y) {
				return "confirm"
			}
			return ""
		}
		if m.overlay == "hosts" && y == 2 && x >= 2 && x < 12 && m.width >= 12 {
			return "action:host-add"
		}
		if m.overlay == "sessions" && y == 2 && x >= 2 && x < 18 && m.width >= 18 {
			rows := m.menuRows()
			if len(rows) > 0 {
				return "close-selected:" + rows[min(m.menuIndex, len(rows)-1)].ID
			}
			return ""
		}
		if m.overlay == "hostedit" {
			if y == 4 {
				if m.height <= 10 && m.hostField == 1 {
					return "host-field:1"
				}
				return "host-field:0"
			}
			if y == 7 && m.height > 10 {
				return "host-field:1"
			}
			if y == 10 && m.height > 13 && x >= 2 && x < 13 {
				return "host-save"
			}
			return ""
		}
		if m.overlay == "prompt" || m.overlay == "help" || m.overlay == "toolinfo" || m.overlay == "startup" {
			return ""
		}
		start := 4
		if m.overlay == "palette" {
			start = 5
		}
		capacity := max(1, m.height-start-3)
		offset := max(0, m.menuIndex-capacity+1)
		i := y - start + offset
		if y >= start && y < start+capacity && i < len(m.menuRows()) {
			return "menu:" + m.overlay + ":" + m.menuRows()[i].ID
		}
		return ""
	}
	for _, b := range m.buttons() {
		r := b.rect()
		if r.x >= 0 && r.x+r.w <= m.width && r.contains(x, y) {
			return "action:" + b.ID
		}
	}
	l := m.layout()
	if l.sidebar > 0 && (paneRect{l.list.x + 1, l.ty, l.list.w - 2, l.th}).contains(x, y) {
		i := y - l.ty + m.scroll
		ids := m.toolIDs()
		if i < len(ids) {
			return "tool:" + ids[i]
		}
	}
	return ""
}
func (m *Model) click(target string) tea.Cmd {
	if target == "host-field:0" {
		return m.focusHostField(0)
	}
	if target == "host-field:1" {
		return m.focusHostField(1)
	}
	if target == "host-save" {
		return m.saveHost()
	}
	if key, ok := strings.CutPrefix(target, "close-selected:"); ok && m.overlay == "sessions" {
		return m.requestClose(key, "sessions")
	}
	if target == "cancel" {
		m.cancelConfirm()
		return nil
	}
	if target == "confirm" {
		return m.confirmApply()
	}
	if s, ok := strings.CutPrefix(target, "menu:"); ok {
		context, id, valid := strings.Cut(s, ":")
		if valid && context == m.overlay {
			for i, row := range m.menuRows() {
				if row.ID == id {
					m.menuIndex = i
					if context == "visibility" {
						m.visibilityToggle(i)
						return nil
					}
					return m.menuAccept()
				}
			}
		}
		return nil
	}
	if id, ok := strings.CutPrefix(target, "tool:"); ok {
		m.selected = id
		return m.activate(id, false)
	}
	if id, ok := strings.CutPrefix(target, "action:"); ok {
		if id == "focus-tool" {
			return m.action(id)
		}
		return m.action(id)
	}
	return nil
}
