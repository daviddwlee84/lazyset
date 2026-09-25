package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
)

const initialConfig = `# lazyset uses built-in tools and sets when this file is empty.
# See "lazyset config show" for the effective configuration.
prefix = 'ctrl+\'
mouse = true
focus_click = "forward"
default_set = "system"

# Keep machine-local SSH hosts in hosts.toml (config edit --hosts).

# Custom tools leave return keys untouched by default. Commands are argument arrays,
# not shell scripts. Set mode = "external" for a full-screen terminal handoff.
# [[tools]]
# id = "editor"
# name = "Neovim"
# command = ["nvim"]
# mode = "embedded"
# return_keys = []

# Built-in sets are read-only; create a copy under a new id to customize one.
# [[sets]]
# id = "my-system"
# name = "My system"
# tools = ["btop", "htop"]
`

const initialHosts = `# Machine-local lazyset hosts; synchronize config.toml independently.
# OpenSSH options and credentials stay in ~/.ssh/config.
default_host = "local"

# [[hosts]]
# id = "server"
# name = "My server"
# ssh = "my-server-alias"
`

// Ensure creates the initial file only when called for an actual editing
// operation. Existing contents are never parsed or replaced, allowing repair.
func Ensure(path string) error { return ensureTemplate(path, initialConfig) }

func EnsureHosts(path string) error { return ensureTemplate(path, initialHosts) }

func ensureTemplate(path, template string) error {
	path, err := Path(path)
	if err != nil {
		return err
	}
	if info, err := os.Stat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("config %s is not a regular file", path)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		// A symlink to a missing file must be repaired through its target, not
		// replaced with a new regular file.
		if info, statErr := os.Stat(path); statErr == nil && info.Mode().IsRegular() {
			return nil
		}
		return fmt.Errorf("config %s already exists but is not a readable regular file", path)
	}
	if err != nil {
		return err
	}
	if _, err := file.WriteString(template); err != nil {
		file.Close()
		os.Remove(path)
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return nil
}

// EditCommand resolves VISUAL, then EDITOR, then vi without shell evaluation.
// The caller checks TTY/JSON intent before calling this function, attaches the
// terminal or uses Bubble Tea's terminal handoff, then calls Load afterward.
func EditCommand(path string) (*exec.Cmd, error) { return editCommand(path, Ensure) }

func EditHostsCommand(path string) (*exec.Cmd, error) { return editCommand(path, EnsureHosts) }

func editCommand(path string, ensure func(string) error) (*exec.Cmd, error) {
	path, err := Path(path)
	if err != nil {
		return nil, err
	}
	editor := strings.TrimSpace(os.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if editor == "" {
		editor = "vi"
	}
	args, err := editorArgs(editor)
	if err != nil {
		return nil, fmt.Errorf("invalid editor command: %w", err)
	}
	executable, err := exec.LookPath(args[0])
	if err != nil {
		return nil, fmt.Errorf("find editor %q: %w", args[0], err)
	}
	if err := ensure(path); err != nil {
		return nil, err
	}
	return exec.Command(executable, append(args[1:], path)...), nil
}

// editorArgs supports normal single/double quoting and escaping while treating
// $, backticks, operators, and globs as literal text. It never invokes a shell.
func editorArgs(command string) ([]string, error) {
	var args []string
	var word strings.Builder
	var quote rune
	started := false
	runes := []rune(command)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == 0 {
			return nil, fmt.Errorf("NUL byte in editor command")
		}
		if quote == '\'' {
			if r == '\'' {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		if r == '\\' {
			if i+1 >= len(runes) {
				return nil, fmt.Errorf("trailing backslash")
			}
			next := runes[i+1]
			if quote == '"' && !strings.ContainsRune("\\\"$`\n", next) {
				word.WriteRune(r)
				continue
			}
			i++
			if next != '\n' {
				word.WriteRune(next)
				started = true
			}
			continue
		}
		if quote == '"' {
			if r == '"' {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		switch {
		case r == '\'' || r == '"':
			quote = r
			started = true
		case unicode.IsSpace(r):
			if started {
				args = append(args, word.String())
				word.Reset()
				started = false
			}
		default:
			word.WriteRune(r)
			started = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed %c quote", quote)
	}
	if started {
		args = append(args, word.String())
	}
	if len(args) == 0 || args[0] == "" {
		return nil, fmt.Errorf("missing editor executable")
	}
	return args, nil
}
