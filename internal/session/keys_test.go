package session

import (
	tea "charm.land/bubbletea/v2"
	"testing"
)

func TestStandardXtermKeys(t *testing.T) {
	tests := []struct {
		name           string
		key            tea.KeyPressMsg
		cursor, keypad bool
		want           string
	}{
		{"arrow", tea.KeyPressMsg{Code: tea.KeyUp}, false, false, "\x1b[A"},
		{"application arrow", tea.KeyPressMsg{Code: tea.KeyUp}, true, false, "\x1bOA"},
		{"modified arrow", tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl | tea.ModShift}, true, false, "\x1b[1;6D"},
		{"control backslash", tea.KeyPressMsg{Code: '\\', Mod: tea.ModCtrl}, false, false, "\x1c"},
		{"control c", tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, false, false, "\x03"},
		{"alt text", tea.KeyPressMsg{Code: 'x', Text: "x", Mod: tea.ModAlt}, false, false, "\x1bx"},
		{"unicode text", tea.KeyPressMsg{Code: tea.KeyExtended, Text: "中文🙂é"}, false, false, "中文🙂é"},
		{"backtab", tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, false, false, "\x1b[Z"},
		{"F12", tea.KeyPressMsg{Code: tea.KeyF12}, false, false, "\x1b[24~"},
		{"keypad", tea.KeyPressMsg{Code: tea.KeyKp5}, false, false, "5"},
		{"application keypad", tea.KeyPressMsg{Code: tea.KeyKp5}, false, true, "\x1bOu"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := encodeKey(test.key, test.cursor, test.keypad); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}
