package session

import (
	"strconv"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

// encodeKey implements legacy xterm input. Extended keyboard protocols are not
// advertised to children. Text is preserved as text, including composed Unicode.
func encodeKey(msg tea.KeyPressMsg, applicationCursor, applicationKeypad bool) string {
	k := msg.Key()
	mod := k.Mod & (tea.ModShift | tea.ModAlt | tea.ModCtrl)
	alt := func(text string) string {
		if mod&tea.ModAlt != 0 {
			return "\x1b" + text
		}
		return text
	}
	param := 1
	if mod&tea.ModShift != 0 {
		param++
	}
	if mod&tea.ModAlt != 0 {
		param += 2
	}
	if mod&tea.ModCtrl != 0 {
		param += 4
	}
	letter := func(final byte) string {
		if mod != 0 {
			return "\x1b[1;" + strconv.Itoa(param) + string(final)
		}
		if applicationCursor {
			return "\x1bO" + string(final)
		}
		return "\x1b[" + string(final)
	}
	tilde := func(n int) string {
		if mod != 0 {
			return "\x1b[" + strconv.Itoa(n) + ";" + strconv.Itoa(param) + "~"
		}
		return "\x1b[" + strconv.Itoa(n) + "~"
	}
	switch k.Code {
	case tea.KeyEnter:
		return alt("\r")
	case tea.KeyTab:
		if mod&tea.ModShift != 0 {
			return alt("\x1b[Z")
		}
		return alt("\t")
	case tea.KeyBackspace:
		if mod&tea.ModCtrl != 0 {
			return alt("\x08")
		}
		return alt("\x7f")
	case tea.KeyEscape:
		return alt("\x1b")
	case tea.KeyUp:
		return letter('A')
	case tea.KeyDown:
		return letter('B')
	case tea.KeyRight:
		return letter('C')
	case tea.KeyLeft:
		return letter('D')
	case tea.KeyHome:
		return letter('H')
	case tea.KeyEnd:
		return letter('F')
	case tea.KeyInsert:
		return tilde(2)
	case tea.KeyDelete:
		return tilde(3)
	case tea.KeyPgUp:
		return tilde(5)
	case tea.KeyPgDown:
		return tilde(6)
	case tea.KeyF1, tea.KeyF2, tea.KeyF3, tea.KeyF4:
		final := byte('P' + k.Code - tea.KeyF1)
		if mod != 0 {
			return "\x1b[1;" + strconv.Itoa(param) + string(final)
		}
		return "\x1bO" + string(final)
	case tea.KeyF5:
		return tilde(15)
	case tea.KeyF6:
		return tilde(17)
	case tea.KeyF7:
		return tilde(18)
	case tea.KeyF8:
		return tilde(19)
	case tea.KeyF9:
		return tilde(20)
	case tea.KeyF10:
		return tilde(21)
	case tea.KeyF11:
		return tilde(23)
	case tea.KeyF12:
		return tilde(24)
	}
	if plain, app, ok := keypad(k.Code); ok {
		if applicationKeypad {
			return alt("\x1bO" + app)
		}
		return alt(plain)
	}
	if mod&tea.ModCtrl != 0 {
		code := unicode.ToLower(k.Code)
		if code >= 'a' && code <= 'z' {
			return alt(string(code - 'a' + 1))
		}
		switch code {
		case tea.KeySpace, '@', '2':
			return alt("\x00")
		case '[':
			return alt("\x1b")
		case '\\':
			return alt("\x1c")
		case ']':
			return alt("\x1d")
		case '^', '6':
			return alt("\x1e")
		case '_', '-':
			return alt("\x1f")
		case '?':
			return alt("\x7f")
		}
	}
	if k.Text != "" {
		return alt(k.Text)
	}
	if k.Code >= ' ' && k.Code <= unicode.MaxRune && unicode.IsPrint(k.Code) {
		return alt(string(k.Code))
	}
	return ""
}

func keypad(code rune) (plain, application string, ok bool) {
	switch code {
	case tea.KeyKp0:
		return "0", "p", true
	case tea.KeyKp1:
		return "1", "q", true
	case tea.KeyKp2:
		return "2", "r", true
	case tea.KeyKp3:
		return "3", "s", true
	case tea.KeyKp4:
		return "4", "t", true
	case tea.KeyKp5:
		return "5", "u", true
	case tea.KeyKp6:
		return "6", "v", true
	case tea.KeyKp7:
		return "7", "w", true
	case tea.KeyKp8:
		return "8", "x", true
	case tea.KeyKp9:
		return "9", "y", true
	case tea.KeyKpEnter:
		return "\r", "M", true
	case tea.KeyKpEqual:
		return "=", "X", true
	case tea.KeyKpMultiply:
		return "*", "j", true
	case tea.KeyKpPlus:
		return "+", "k", true
	case tea.KeyKpComma:
		return ",", "l", true
	case tea.KeyKpMinus:
		return "-", "m", true
	case tea.KeyKpDecimal:
		return ".", "n", true
	case tea.KeyKpDivide:
		return "/", "o", true
	}
	return "", "", false
}
