package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
	"lazyset/internal/core"
)

// Sources keeps portable preferences separate from this machine's SSH hosts.
// MainPath is the logical path: resolving a dotfile symlink must not move hosts
// into the dotfiles repository beside its target.
type Sources struct {
	MainPath, HostsPath         string
	MainExplicit, HostsExplicit bool
}

func HostsPath(main string) (string, error) {
	path, err := Path(main)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), "hosts.toml"), nil
}

func ResolveSources(main, hosts string, mainExplicit, hostsExplicit bool) (Sources, error) {
	mainPath, err := Path(main)
	if err != nil {
		return Sources{}, err
	}
	hostsPath := hosts
	if hostsPath == "" {
		hostsPath, err = HostsPath(mainPath)
	} else {
		hostsPath, err = filepath.Abs(hostsPath)
	}
	if err != nil {
		return Sources{}, err
	}
	if mainPath == hostsPath {
		return Sources{}, fmt.Errorf("config and hosts config must use different files")
	}
	return Sources{mainPath, hostsPath, mainExplicit, hostsExplicit}, nil
}

type hostsDocument struct {
	DefaultHost *string   `toml:"default_host"`
	Hosts       []hostDoc `toml:"hosts"`
}

func readSource(path string, explicit bool) ([]byte, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) && !explicit {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	return data, nil
}

func LoadSources(sources Sources) (core.Config, error) {
	s, err := ResolveSources(sources.MainPath, sources.HostsPath, sources.MainExplicit, sources.HostsExplicit)
	if err != nil {
		return core.Config{}, err
	}
	if err := distinctSourceFiles(s); err != nil {
		return core.Config{}, err
	}
	main, err := readSource(s.MainPath, s.MainExplicit)
	if err != nil {
		return core.Config{}, err
	}
	hosts, err := readSource(s.HostsPath, s.HostsExplicit)
	if err != nil {
		return core.Config{}, err
	}
	return loadSourceBytes(s, main, hosts)
}

func distinctSourceFiles(s Sources) error {
	main, mainErr := os.Stat(s.MainPath)
	hosts, hostsErr := os.Stat(s.HostsPath)
	if mainErr == nil && hostsErr == nil && os.SameFile(main, hosts) {
		return fmt.Errorf("config and hosts config resolve to the same file; choose separate files")
	}
	return nil
}

func loadSourceBytes(s Sources, main, hosts []byte) (core.Config, error) {
	var doc document
	if err := toml.NewDecoder(bytes.NewReader(main)).DisallowUnknownFields().Decode(&doc); err != nil {
		return core.Config{}, fmt.Errorf("config %s: invalid TOML: %w (repair with lazyset --config %q config edit)", s.MainPath, err, s.MainPath)
	}
	var h hostsDocument
	if err := toml.NewDecoder(bytes.NewReader(hosts)).DisallowUnknownFields().Decode(&h); err != nil {
		return core.Config{}, fmt.Errorf("hosts config %s: invalid TOML: %w (repair with lazyset --config %q --hosts-config %q config edit --hosts)", s.HostsPath, err, s.MainPath, s.HostsPath)
	}
	cfg, err := applyDocuments(doc, h)
	if err != nil {
		return core.Config{}, fmt.Errorf("validate config %s and hosts %s: %w", s.MainPath, s.HostsPath, err)
	}
	return cfg, nil
}
