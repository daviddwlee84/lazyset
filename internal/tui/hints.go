package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

func prefixActionKey(id string) string {
	for _, a := range prefixActions {
		if a.ID == id {
			if a.Key == "esc" {
				return "Esc"
			}
			return a.Key
		}
	}
	return ""
}

func (m *Model) returnSequence() string {
	return prefixLabel(m.cfg.Prefix) + " → " + prefixActionKey("observe")
}

// Keep the full return sequence ahead of secondary labels on narrow terminals.
func (m *Model) returnHint(width int) string {
	sequence := m.returnSequence()
	for _, hint := range []string{sequence + ": lazyset", sequence + ": back"} {
		if ansi.StringWidth(hint) <= width {
			return hint
		}
	}
	return sequence
}

// Hints are ordered by priority. Secondary segments are included whole, so a
// clipped shortcut cannot look like an actionable binding.
func hintSegments(width int, segments ...string) string {
	var line string
	for _, segment := range segments {
		if segment == "" {
			continue
		}
		candidate := segment
		if line != "" {
			candidate = line + " · " + segment
		}
		if ansi.StringWidth(candidate) <= width {
			line = candidate
		}
	}
	return line
}

func (m *Model) footer(width int) string {
	var segments []string
	switch {
	case m.literalNext:
		segments = []string{"NEXT KEY → tool", "then " + m.returnHint(width), "bypass return_keys"}
	case m.prefix:
		segments = []string{prefixActionKey("observe") + ": lazyset", "n/p switch", ": commands", "x close", "v literal next", "Q quit"}
	case m.filtering:
		segments = []string{"Type filter", "↑↓ choose", "Enter finish filter", "Esc clear"}
	case m.interact:
		segments = []string{m.returnHint(width)}
		if r := m.current(); r != nil {
			if keys := effectiveReturnKeys(r.Tool); len(keys) > 0 {
				segments = append(segments, strings.Join(keys, "/")+" return")
			}
		}
		prefix := prefixLabel(m.cfg.Prefix)
		segments = append(segments, prefix+" v: literal next", prefix+" : commands")
		if m.cfg.Mouse {
			segments = append(segments, "mouse [Quit]")
		}
	default:
		segments = []string{"↑↓/jk select", "Enter view/interact", "Space/: commands", "f status", "s sessions", "x close"}
	}
	segments = append(segments, fmt.Sprintf("%d running", len(m.liveKeys())))
	return hintSegments(width, segments...)
}
