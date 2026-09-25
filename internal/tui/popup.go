package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type confirmationGeometry struct {
	pane                    paneRect
	cancel, accept          paneRect
	bodyX, bodyY, bodyWidth int
	bodyRows                int
	lines                   []string
}

// Confirmation layout is shared by rendering and input, including stacked
// buttons on narrow screens. It never depends on an earlier View call.
func (m *Model) confirmationLayout() confirmationGeometry {
	w := min(68, max(1, m.width-4))
	bodyWidth := max(1, w-4)
	var body []string
	for _, line := range m.confirmLines {
		body = append(body, strings.Split(ansi.Wrap(clean(line), bodyWidth, ""), "\n")...)
	}
	buttonRows := 1
	if bodyWidth < 23 {
		buttonRows = 2
	}
	h := min(max(1, m.height-2), max(buttonRows+4, len(body)+buttonRows+3))
	pane := paneRect{max(0, (m.width-w)/2), max(0, (m.height-h)/2), w, h}
	buttonsY := pane.y + pane.h - 1 - buttonRows
	g := confirmationGeometry{pane: pane, bodyX: pane.x + 2, bodyY: pane.y + 1, bodyWidth: bodyWidth, bodyRows: max(0, h-buttonRows-3), lines: body}
	if buttonRows == 1 {
		x := pane.x + (pane.w-23)/2
		g.cancel = paneRect{x, buttonsY, 10, 1}
		g.accept = paneRect{x + 12, buttonsY, 11, 1}
	} else {
		g.cancel = paneRect{pane.x + (pane.w-10)/2, buttonsY, 10, 1}
		g.accept = paneRect{pane.x + (pane.w-11)/2, buttonsY + 1, 11, 1}
	}
	return g
}

func (m *Model) confirmView() string {
	if m.height < 9 || m.width < 24 {
		return m.smallView()
	}
	// Rendering a copy preserves the underlying menu selection without changing
	// the live confirmation's button index or exposing input to that menu.
	base := *m
	base.overlay, base.menuIndex = m.confirmReturn, m.confirmIndex
	content := base.mainView()
	if base.overlay != "" {
		content = base.overlayView()
	}
	lines := strings.Split(content, "\n")
	g := m.confirmationLayout()
	for row := g.pane.y; row < g.pane.y+g.pane.h; row++ {
		putCells(lines, g.pane.x, row, strings.Repeat(" ", g.pane.w))
	}
	m.drawFrame(lines, g.pane, "Confirm: "+m.confirmAction, true)
	for i := 0; i < g.bodyRows && i < len(g.lines); i++ {
		line := g.lines[i]
		if i > 0 && i == g.bodyRows-1 && len(g.lines) > g.bodyRows {
			line = fmt.Sprintf("… %d more lines", len(g.lines)-i)
		}
		putCells(lines, g.bodyX, g.bodyY+i, fit(line, g.bodyWidth))
	}
	cancel, accept := "[ Cancel ]", "[ Confirm ]"
	if m.menuIndex == 0 {
		cancel = m.style(cancel, "selected")
	} else {
		accept = m.style(accept, "selected")
	}
	putCells(lines, g.cancel.x, g.cancel.y, cancel)
	putCells(lines, g.accept.x, g.accept.y, accept)
	lines[m.height-1] = fit("←→/Tab choose · Enter accept · Esc cancel", m.width)
	return strings.Join(lines, "\n")
}
