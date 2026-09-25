package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/lazyset/internal/catalog"
	"github.com/daviddwlee84/lazyset/internal/core"
	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// ErrChanged means the file changed after the UI began editing. Reload it before
// retrying; never overwrite an unrelated editor's changes silently.
var ErrChanged = errors.New("config changed on disk; reload before saving")

// Snapshot returns exact file contents. nil means a missing file; an existing
// empty file returns a non-nil empty slice. Pass this to the guarded write APIs.
func Snapshot(path string) ([]byte, error) {
	path, err := Path(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config snapshot: %w", err)
	}
	if data == nil {
		data = []byte{}
	}
	return data, nil
}

func SaveSet(path string, set core.Set) error {
	before, err := Snapshot(path)
	if err != nil {
		return err
	}
	return SaveSetIfUnchanged(path, set, before)
}

// SaveSetIfUnchanged changes only this custom set's values, preserving unrelated
// TOML and comments. Built-in sets must be copied under a new id first.
func SaveSetIfUnchanged(path string, set core.Set, expected []byte) error {
	return saveSetIfUnchanged(path, set, expected, validateBytes)
}

func saveSetIfUnchanged(path string, set core.Set, expected []byte, validate func([]byte) error) error {
	if set.Builtin || catalog.IsBuiltinSet(set.ID) {
		return fmt.Errorf("set %q is built in; copy it with a new id before editing", set.ID)
	}
	if set.Name == "" {
		set.Name = set.ID
	}
	if err := identity("set", set.ID, set.Name, map[string]bool{}); err != nil {
		return err
	}
	return updateFileValidated(path, expected, func(data []byte) ([]byte, error) {
		if err := validate(data); err != nil {
			return nil, err
		}
		blocks, err := setBlocks(data)
		if err != nil {
			return nil, err
		}
		for _, b := range blocks {
			if b.id != set.ID {
				continue
			}
			var edits []replacement
			var appendFields strings.Builder
			for _, field := range []struct {
				key   string
				value any
			}{{"name", set.Name}, {"tools", nonnil(set.Tools)}} {
				value, err := tomlValue(field.value)
				if err != nil {
					return nil, err
				}
				if span, ok := b.values[field.key]; ok {
					edits = append(edits, replacement{span, value})
				} else {
					fmt.Fprintf(&appendFields, "%s = %s\n", field.key, value)
				}
			}
			if appendFields.Len() > 0 {
				text := appendFields.String()
				if b.insert > 0 && data[b.insert-1] != '\n' {
					text = "\n" + text
				}
				edits = append(edits, replacement{span{b.insert, b.insert}, text})
			}
			return applyReplacements(data, edits), nil
		}
		encoded, err := toml.Marshal(struct {
			Sets []setDoc `toml:"sets"`
		}{[]setDoc{{ID: set.ID, Name: set.Name, Tools: nonnil(set.Tools)}}})
		if err != nil {
			return nil, err
		}
		result := bytes.Clone(data)
		if len(result) > 0 && result[len(result)-1] != '\n' {
			result = append(result, '\n')
		}
		if len(result) > 0 {
			result = append(result, '\n')
		}
		return append(result, encoded...), nil
	}, validate)
}

func DeleteSet(path, id string) error {
	before, err := Snapshot(path)
	if err != nil {
		return err
	}
	return DeleteSetIfUnchanged(path, id, before)
}

// DeleteSetIfUnchanged removes a custom set, leaving standalone comments and
// all other configuration intact. A default_set reference must be changed first.
func DeleteSetIfUnchanged(path, id string, expected []byte) error {
	return deleteSetIfUnchanged(path, id, expected, validateBytes)
}

func deleteSetIfUnchanged(path, id string, expected []byte, validate func([]byte) error) error {
	if catalog.IsBuiltinSet(id) {
		return fmt.Errorf("set %q is built in and cannot be deleted", id)
	}
	return updateFileValidated(path, expected, func(data []byte) ([]byte, error) {
		if err := validate(data); err != nil {
			return nil, err
		}
		blocks, err := setBlocks(data)
		if err != nil {
			return nil, err
		}
		for _, b := range blocks {
			if b.id != id {
				continue
			}
			edits := make([]replacement, 0, len(b.expressions))
			for _, expression := range b.expressions {
				edits = append(edits, replacement{expression, ""})
			}
			return applyReplacements(data, edits), nil
		}
		return nil, fmt.Errorf("custom set %q does not exist", id)
	}, validate)
}

func nonnil(items []string) []string {
	if items == nil {
		return []string{}
	}
	return items
}

type span struct{ start, end int }
type replacement struct {
	span
	text string
}
type setBlock struct {
	id          string
	values      map[string]span
	expressions []span
	insert      int
}

// The TOML parser supplies expression boundaries, so text resembling a table
// inside a multiline string cannot be mistaken for an actual set declaration.
func setBlocks(data []byte) ([]setBlock, error) {
	var parser unstable.Parser
	parser.Reset(data)
	var blocks []setBlock
	active := -1
	root := true
	for parser.NextExpression() {
		n := parser.Expression()
		switch n.Kind {
		case unstable.Table, unstable.ArrayTable:
			root = false
			active = -1
			keys := nodeKeys(n)
			if n.Kind != unstable.ArrayTable || len(keys) != 1 || keys[0] != "sets" {
				continue
			}
			key := n.Child()
			start, end := lineStart(data, int(key.Raw.Offset)), lineEnd(data, int(key.Raw.Offset+key.Raw.Length))
			blocks = append(blocks, setBlock{values: map[string]span{}, expressions: []span{{start, end}}, insert: end})
			active = len(blocks) - 1
		case unstable.KeyValue:
			keys := nodeKeys(n)
			if root && len(keys) > 0 && keys[0] == "sets" {
				return nil, fmt.Errorf("edit inline sets manually with config edit, or use [[sets]] tables for UI editing")
			}
			if active < 0 || len(keys) != 1 {
				continue
			}
			b := &blocks[active]
			start, end := int(n.Raw.Offset), int(n.Raw.Offset+n.Raw.Length)
			// Known set keys are single keys; search from the raw end of that
			// key rather than mistaking '=' inside a quoted key for a separator.
			keyIter := n.Key()
			keyIter.Next()
			keyNode := keyIter.Node()
			valueStart := int(keyNode.Raw.Offset + keyNode.Raw.Length)
			for valueStart < end && data[valueStart] != '=' {
				valueStart++
			}
			valueStart++
			for valueStart < end && (data[valueStart] == ' ' || data[valueStart] == '\t') {
				valueStart++
			}
			b.values[keys[0]] = span{valueStart, end}
			full := span{lineStart(data, start), lineEnd(data, end)}
			b.expressions = append(b.expressions, full)
			b.insert = full.end
			if keys[0] == "id" {
				b.id = string(n.Value().Data)
			}
		}
	}
	if err := parser.Error(); err != nil {
		return nil, fmt.Errorf("parse set locations: %w", err)
	}
	return blocks, nil
}

func nodeKeys(n *unstable.Node) []string {
	var keys []string
	for it := n.Key(); it.Next(); {
		keys = append(keys, string(it.Node().Data))
	}
	return keys
}

func lineStart(data []byte, offset int) int {
	for offset > 0 && data[offset-1] != '\n' {
		offset--
	}
	return offset
}

func lineEnd(data []byte, offset int) int {
	for offset < len(data) && data[offset] != '\n' {
		offset++
	}
	if offset < len(data) {
		offset++
	}
	return offset
}

func tomlValue(value any) (string, error) {
	data, err := toml.Marshal(map[string]any{"value": value})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.TrimPrefix(string(data), "value = ")), nil
}

func applyReplacements(data []byte, edits []replacement) []byte {
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	result := bytes.Clone(data)
	for _, edit := range edits {
		part := append([]byte{}, result[:edit.start]...)
		part = append(part, edit.text...)
		result = append(part, result[edit.end:]...)
	}
	return result
}

func sameSnapshot(a, b []byte) bool { return (a == nil) == (b == nil) && bytes.Equal(a, b) }

func validateBytes(data []byte) error { _, err := loadBytes(data); return err }

// updateFile serializes lazyset's own writers and compares the exact snapshot
// immediately before the atomic replacement. The snapshot also protects an
// editor that opened before another CLI/TUI instance saved.
func updateFile(path string, expected []byte, update func([]byte) ([]byte, error)) error {
	return updateFileValidated(path, expected, update, validateBytes)
}

func updateFileValidated(path string, expected []byte, update func([]byte) ([]byte, error), validate func([]byte) error) error {
	resolved, err := Path(path)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(resolved); err == nil && info.Mode()&os.ModeSymlink != 0 {
		resolved, err = filepath.EvalSymlinks(resolved)
		if err != nil {
			return fmt.Errorf("resolve config symlink: %w", err)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	before, err := Snapshot(resolved)
	if err != nil {
		return err
	}
	if !sameSnapshot(before, expected) {
		return ErrChanged
	}
	updated, err := update(before)
	if err != nil {
		return err
	}
	if err := validate(updated); err != nil {
		return fmt.Errorf("updated config would be invalid: %w", err)
	}
	if bytes.Equal(before, updated) && before != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(resolved+".lock", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("config is being saved by another process (lock %s); retry after it finishes", resolved+".lock")
		}
		return err
	}
	defer os.Remove(resolved + ".lock")
	if err := lock.Close(); err != nil {
		return err
	}
	current, err := Snapshot(resolved)
	if err != nil {
		return err
	}
	if !sameSnapshot(current, expected) {
		return ErrChanged
	}
	mode := os.FileMode(0600)
	if info, err := os.Stat(resolved); err == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(resolved), ".lazyset-config-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(updated); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	current, err = Snapshot(resolved)
	if err != nil {
		return err
	}
	if !sameSnapshot(current, expected) {
		return ErrChanged
	}
	if err := validate(updated); err != nil {
		return fmt.Errorf("updated config would be invalid: %w", err)
	}
	if err := os.Rename(tmpName, resolved); err != nil {
		return err
	}
	return nil
}
