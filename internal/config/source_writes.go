package config

import (
	"bytes"
	"fmt"

	"github.com/daviddwlee84/lazyset/internal/core"
	"github.com/pelletier/go-toml/v2"
)

// sourceWriteValidator validates the merged candidate and rejects changes to
// its companion file during the write. The destination's own exact snapshot
// and atomic replacement remain guarded by updateFileValidated.
func sourceWriteValidator(s Sources, hostsTarget bool) (func([]byte) error, error) {
	if err := distinctSourceFiles(s); err != nil {
		return nil, err
	}
	other, explicit := s.HostsPath, s.HostsExplicit
	if hostsTarget {
		other, explicit = s.MainPath, s.MainExplicit
	}
	snapshot, err := readSource(other, explicit)
	if err != nil {
		return nil, err
	}
	return func(data []byte) error {
		if err := distinctSourceFiles(s); err != nil {
			return err
		}
		current, err := readSource(other, explicit)
		if err != nil {
			return err
		}
		if !sameSnapshot(current, snapshot) {
			return ErrChanged
		}
		main, hosts := data, snapshot
		if hostsTarget {
			main, hosts = snapshot, data
		}
		_, err = loadSourceBytes(s, main, hosts)
		return err
	}, nil
}

func SaveSetSourcesIfUnchanged(sources Sources, set core.Set, expected []byte) error {
	s, err := ResolveSources(sources.MainPath, sources.HostsPath, sources.MainExplicit, sources.HostsExplicit)
	if err != nil {
		return err
	}
	validate, err := sourceWriteValidator(s, false)
	if err != nil {
		return err
	}
	return saveSetIfUnchanged(s.MainPath, set, expected, validate)
}

func DeleteSetSourcesIfUnchanged(sources Sources, id string, expected []byte) error {
	s, err := ResolveSources(sources.MainPath, sources.HostsPath, sources.MainExplicit, sources.HostsExplicit)
	if err != nil {
		return err
	}
	validate, err := sourceWriteValidator(s, false)
	if err != nil {
		return err
	}
	return deleteSetIfUnchanged(s.MainPath, id, expected, validate)
}

// SaveHostSourcesIfUnchanged appends only to hosts.toml. Duplicate checking uses
// the effective merged catalog, including hosts still stored in legacy config.
func SaveHostSourcesIfUnchanged(sources Sources, h core.Host, expected []byte) error {
	s, err := ResolveSources(sources.MainPath, sources.HostsPath, sources.MainExplicit, sources.HostsExplicit)
	if err != nil {
		return err
	}
	validate, err := sourceWriteValidator(s, true)
	if err != nil {
		return err
	}
	if h.Name == "" {
		h.Name = h.ID
	}
	return updateFileValidated(s.HostsPath, expected, func(data []byte) ([]byte, error) {
		if err := validate(data); err != nil {
			return nil, err
		}
		main, err := readSource(s.MainPath, s.MainExplicit)
		if err != nil {
			return nil, err
		}
		cfg, err := loadSourceBytes(s, main, data)
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
		encoded, err := toml.Marshal(hostsDocument{Hosts: []hostDoc{{ID: h.ID, Name: &h.Name, SSH: &h.SSH, Env: h.Env}}})
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
