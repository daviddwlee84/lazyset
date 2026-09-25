package config

import (
	"bytes"
	"fmt"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
	"lazyset/internal/core"
)

// SaveHostIfUnchanged adds a new SSH host without rewriting existing TOML or
// comments. Existing hosts, including local, cannot be replaced by this API.
// expected is the exact Snapshot from when editing began; nil means no file.
func SaveHostIfUnchanged(path string, h core.Host, expected []byte) error {
	if h.Name == "" {
		h.Name = h.ID
	}
	return updateFile(path, expected, func(data []byte) ([]byte, error) {
		cfg, err := loadBytes(data)
		if err != nil {
			return nil, err
		}
		if _, exists := cfg.Host(h.ID); exists {
			return nil, fmt.Errorf("host %q already exists; choose a new id", h.ID)
		}
		cfg.Hosts = append(cfg.Hosts, h)
		if err := Validate(cfg); err != nil {
			return nil, err
		}
		if err := checkHostTableFormat(data); err != nil {
			return nil, err
		}
		encoded, err := toml.Marshal(struct {
			Hosts []hostDoc `toml:"hosts"`
		}{[]hostDoc{{ID: h.ID, Name: &h.Name, SSH: &h.SSH, Env: h.Env}}})
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
	})
}

// Appending a [[hosts]] table cannot extend a root inline hosts array. Report
// this deliberately rather than rewriting the user's existing representation.
func checkHostTableFormat(data []byte) error {
	var parser unstable.Parser
	parser.Reset(data)
	for parser.NextExpression() {
		n := parser.Expression()
		switch n.Kind {
		case unstable.Table, unstable.ArrayTable:
			return nil
		case unstable.KeyValue:
			keys := nodeKeys(n)
			if len(keys) > 0 && keys[0] == "hosts" {
				return fmt.Errorf("add inline hosts manually with config edit, or use [[hosts]] tables for UI editing")
			}
		}
	}
	return parser.Error()
}
