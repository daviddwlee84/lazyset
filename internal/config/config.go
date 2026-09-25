// Package config implements lazyset's XDG configuration and effective defaults.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/pelletier/go-toml/v2"
	"lazyset/internal/catalog"
	"lazyset/internal/core"
)

// Defaults always returns independently owned slices and maps.
func Defaults() core.Config {
	return core.Config{
		Prefix: "ctrl+\\", Mouse: true, FocusClick: "forward", DefaultHost: "local", DefaultSet: "system",
		Hosts: []core.Host{{ID: "local", Name: "Local"}}, Tools: catalog.Tools(), Sets: catalog.Sets(),
	}
}

// Path resolves a requested path or the XDG configuration location. Resolution
// does not create directories or require the configuration to parse.
func Path(override string) (string, error) {
	if override != "" {
		return filepath.Abs(override)
	}
	dir, err := xdgDir("XDG_CONFIG_HOME", ".config")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "lazyset", "config.toml"), nil
}

func StateDir() (string, error) {
	dir, err := xdgDir("XDG_STATE_HOME", ".local/state")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "lazyset"), nil
}

func CacheDir() (string, error) {
	dir, err := xdgDir("XDG_CACHE_HOME", ".cache")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "lazyset"), nil
}

func xdgDir(variable, fallback string) (string, error) {
	if value := os.Getenv(variable); filepath.IsAbs(value) {
		return filepath.Clean(value), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return "", fmt.Errorf("resolve %s: set an absolute %s or HOME", variable, variable)
	}
	return filepath.Join(home, filepath.FromSlash(fallback)), nil
}

// Load merges a TOML file over the built-in catalog. Omitted values inherit
// defaults; explicitly supplied false and empty slices are not lost.
func Load(path string, explicit bool) (core.Config, error) {
	sources, err := ResolveSources(path, "", explicit, false)
	if err != nil {
		return core.Config{}, err
	}
	return LoadSources(sources)
}

type document struct {
	Prefix      *string   `toml:"prefix"`
	Mouse       *bool     `toml:"mouse"`
	FocusClick  *string   `toml:"focus_click"`
	DefaultHost *string   `toml:"default_host"`
	DefaultSet  *string   `toml:"default_set"`
	Hosts       []hostDoc `toml:"hosts"`
	Tools       []toolDoc `toml:"tools"`
	Sets        []setDoc  `toml:"sets"`
}

type hostDoc struct {
	ID   string            `toml:"id"`
	Name *string           `toml:"name"`
	SSH  *string           `toml:"ssh"`
	Env  map[string]string `toml:"env"`
}

type toolDoc struct {
	ID          string            `toml:"id"`
	Name        *string           `toml:"name"`
	Description *string           `toml:"description"`
	Category    *string           `toml:"category"`
	Command     *[]string         `toml:"command"`
	Candidates  *[]string         `toml:"candidates"`
	Platforms   *[]string         `toml:"platforms"`
	Mode        *string           `toml:"mode"`
	QToObserve  *bool             `toml:"q_to_observe"`
	ReturnKeys  *[]string         `toml:"return_keys"`
	Dir         *string           `toml:"dir"`
	Env         map[string]string `toml:"env"`
	InstallURL  *string           `toml:"install_url"`
	Hint        *string           `toml:"hint"`
}

type setDoc struct {
	ID    string   `toml:"id"`
	Name  string   `toml:"name"`
	Tools []string `toml:"tools"`
}

func loadBytes(data []byte) (core.Config, error) {
	var doc document
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&doc); err != nil {
		return core.Config{}, fmt.Errorf("invalid TOML: %w", err)
	}
	return applyDocuments(doc, hostsDocument{})
}

func applyDocuments(doc document, hosts hostsDocument) (core.Config, error) {
	cfg := Defaults()
	assign(&cfg.FocusClick, doc.FocusClick)
	assign(&cfg.Prefix, doc.Prefix)
	assign(&cfg.Mouse, doc.Mouse)
	assign(&cfg.DefaultHost, doc.DefaultHost)
	assign(&cfg.DefaultSet, doc.DefaultSet)
	cfg.Prefix = strings.ToLower(cfg.Prefix)
	seen := map[string]bool{}
	for _, h := range doc.Hosts {
		if seen[h.ID] {
			return core.Config{}, fmt.Errorf("duplicate host id %q", h.ID)
		}
		seen[h.ID] = true
		host, exists := cfg.Host(h.ID)
		if !exists {
			host = core.Host{ID: h.ID, Name: h.ID}
		}
		assign(&host.Name, h.Name)
		assign(&host.SSH, h.SSH)
		host.Env = mergeEnv(host.Env, h.Env)
		if exists {
			for i := range cfg.Hosts {
				if cfg.Hosts[i].ID == h.ID {
					cfg.Hosts[i] = host
				}
			}
		} else {
			cfg.Hosts = append(cfg.Hosts, host)
		}
	}
	if len(doc.Hosts) > 0 || doc.DefaultHost != nil {
		cfg.Warnings = append(cfg.Warnings, "Legacy host settings in config.toml: move default_host and [[hosts]] to hosts.toml; use config edit --hosts.")
	}
	// Machine-local records replace legacy entries as whole records, so old
	// credentials/environment overrides cannot leak into a replacement target.
	seen = map[string]bool{}
	for _, h := range hosts.Hosts {
		if seen[h.ID] {
			return core.Config{}, fmt.Errorf("duplicate host id %q in hosts file", h.ID)
		}
		seen[h.ID] = true
		host := core.Host{ID: h.ID, Name: h.ID}
		if h.ID == "local" {
			host.Name = "Local"
		}
		assign(&host.Name, h.Name)
		assign(&host.SSH, h.SSH)
		host.Env = mergeEnv(nil, h.Env)
		replaced := false
		for i := range cfg.Hosts {
			if cfg.Hosts[i].ID == h.ID {
				cfg.Hosts[i] = host
				replaced = true
				break
			}
		}
		if !replaced {
			cfg.Hosts = append(cfg.Hosts, host)
		}
	}
	assign(&cfg.DefaultHost, hosts.DefaultHost)
	seen = map[string]bool{}
	for _, t := range doc.Tools {
		if seen[t.ID] {
			return core.Config{}, fmt.Errorf("duplicate tool id %q", t.ID)
		}
		seen[t.ID] = true
		tool, exists := cfg.Tool(t.ID)
		if !exists {
			tool = core.Tool{ID: t.ID, Name: t.ID, Category: "Custom", Mode: "embedded"}
		}
		assign(&tool.Name, t.Name)
		assign(&tool.Description, t.Description)
		assign(&tool.Category, t.Category)
		assign(&tool.Command, t.Command)
		// A changed command must not inherit candidate names for a different
		// executable (notably gdu versus Homebrew's gdu-go).
		if t.Command != nil && t.Candidates == nil {
			tool.Candidates = nil
		}
		assign(&tool.Candidates, t.Candidates)
		assign(&tool.Platforms, t.Platforms)
		assign(&tool.Mode, t.Mode)
		if t.ReturnKeys != nil && t.QToObserve != nil {
			return core.Config{}, fmt.Errorf("tool %q: use return_keys or legacy q_to_observe, not both", t.ID)
		}
		if t.ReturnKeys != nil {
			tool.ReturnKeys = append([]string{}, (*t.ReturnKeys)...)
		}
		if t.QToObserve != nil {
			if !*t.QToObserve {
				tool.ReturnKeys = slices.DeleteFunc(tool.ReturnKeys, func(key string) bool { return key == "q" })
			} else if !slices.Contains(tool.ReturnKeys, "q") {
				tool.ReturnKeys = append(tool.ReturnKeys, "q")
			}
		}
		tool.QToObserve = slices.Contains(tool.ReturnKeys, "q")
		assign(&tool.Dir, t.Dir)
		assign(&tool.InstallURL, t.InstallURL)
		assign(&tool.Hint, t.Hint)
		tool.Env = mergeEnv(tool.Env, t.Env)
		if exists {
			for i := range cfg.Tools {
				if cfg.Tools[i].ID == t.ID {
					cfg.Tools[i] = tool
				}
			}
		} else {
			cfg.Tools = append(cfg.Tools, tool)
		}
	}
	// All follows the effective tool catalog, including user additions. Other
	// sets retain their explicit membership, including copies previously made
	// from All.
	for i := range cfg.Sets {
		if cfg.Sets[i].ID == catalog.AllSetID {
			cfg.Sets[i] = catalog.AllSet(cfg.Tools)
			break
		}
	}
	seen = map[string]bool{}
	for _, s := range doc.Sets {
		if seen[s.ID] {
			return core.Config{}, fmt.Errorf("duplicate set id %q", s.ID)
		}
		seen[s.ID] = true
		if catalog.IsBuiltinSet(s.ID) {
			return core.Config{}, fmt.Errorf("set %q is built in; copy it with a new id before editing", s.ID)
		}
		if s.Name == "" {
			s.Name = s.ID
		}
		cfg.Sets = append(cfg.Sets, core.Set{ID: s.ID, Name: s.Name, Tools: s.Tools})
	}
	if err := Validate(cfg); err != nil {
		return core.Config{}, err
	}
	return cfg, nil
}

func assign[T any](dst *T, src *T) {
	if src != nil {
		*dst = *src
	}
}

func mergeEnv(base, extra map[string]string) map[string]string {
	if len(base)+len(extra) == 0 {
		return nil
	}
	result := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		result[k] = v
	}
	for k, v := range extra {
		result[k] = v
	}
	return result
}

var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var validEnv = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Validate checks cross references and execution data before any process starts.
func Validate(cfg core.Config) error {
	if cfg.FocusClick != "forward" && cfg.FocusClick != "focus-only" {
		return fmt.Errorf("focus_click must be forward or focus-only")
	}
	if !validPrefix(cfg.Prefix) {
		return fmt.Errorf("unsupported prefix %q: use ctrl+a..ctrl+z, ctrl+space, ctrl+[, ctrl+\\, ctrl+], ctrl+^, or ctrl+_", cfg.Prefix)
	}
	hosts, tools, sets := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, h := range cfg.Hosts {
		if err := identity("host", h.ID, h.Name, hosts); err != nil {
			return err
		}
		if h.ID == "local" && h.SSH != "" {
			return fmt.Errorf("host local cannot have an ssh target")
		}
		if h.ID != "local" && (h.SSH == "" || strings.HasPrefix(h.SSH, "-") || strings.ContainsFunc(h.SSH, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })) {
			return fmt.Errorf("host %q needs one OpenSSH alias or user@host in ssh (put options in ~/.ssh/config)", h.ID)
		}
		if err := validateEnv(h.Env); err != nil {
			return fmt.Errorf("host %q: %w", h.ID, err)
		}
	}
	if !hosts["local"] {
		return fmt.Errorf("local host is required")
	}
	if !hosts[cfg.DefaultHost] {
		return fmt.Errorf("default_host %q does not name a configured host", cfg.DefaultHost)
	}
	for _, t := range cfg.Tools {
		if err := identity("tool", t.ID, t.Name, tools); err != nil {
			return err
		}
		if len(t.Command) == 0 || strings.TrimSpace(t.Command[0]) == "" {
			return fmt.Errorf("tool %q needs command = [executable, ...]", t.ID)
		}
		keys := map[string]bool{}
		for _, key := range t.ReturnKeys {
			if !validReturnKey(key) {
				return fmt.Errorf("tool %q: return_keys entry %q must be one canonical key (for example q, Q, ctrl+c, esc, or f10)", t.ID, key)
			}
			if keys[key] {
				return fmt.Errorf("tool %q: duplicate return_keys entry %q", t.ID, key)
			}
			keys[key] = true
		}
		if t.Mode != "embedded" && t.Mode != "external" {
			return fmt.Errorf("tool %q: mode must be embedded or external", t.ID)
		}
		for _, arg := range t.Command {
			if strings.ContainsRune(arg, 0) {
				return fmt.Errorf("tool %q command contains a NUL byte", t.ID)
			}
		}
		for _, candidate := range t.Candidates {
			if candidate == "" || strings.ContainsRune(candidate, 0) {
				return fmt.Errorf("tool %q has an empty or invalid candidate executable", t.ID)
			}
		}
		if strings.ContainsRune(t.Dir, 0) {
			return fmt.Errorf("tool %q dir contains a NUL byte", t.ID)
		}
		if err := validateEnv(t.Env); err != nil {
			return fmt.Errorf("tool %q: %w", t.ID, err)
		}
		for _, platform := range t.Platforms {
			if platform != "darwin" && platform != "linux" {
				return fmt.Errorf("tool %q: platform %q must be darwin or linux", t.ID, platform)
			}
		}
	}
	for _, s := range cfg.Sets {
		if err := identity("set", s.ID, s.Name, sets); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, toolID := range s.Tools {
			if !tools[toolID] {
				return fmt.Errorf("set %q refers to unknown tool %q", s.ID, toolID)
			}
			if seen[toolID] {
				return fmt.Errorf("set %q repeats tool %q", s.ID, toolID)
			}
			seen[toolID] = true
		}
	}
	if !sets[cfg.DefaultSet] {
		return fmt.Errorf("default_set %q does not name a configured set", cfg.DefaultSet)
	}
	return nil
}

func identity(kind, id, name string, seen map[string]bool) error {
	if !validID.MatchString(id) {
		return fmt.Errorf("%s id %q must use letters, numbers, dots, underscores, or dashes", kind, id)
	}
	if seen[id] {
		return fmt.Errorf("duplicate %s id %q", kind, id)
	}
	seen[id] = true
	if strings.TrimSpace(name) == "" || strings.ContainsFunc(name, unicode.IsControl) {
		return fmt.Errorf("%s %q needs a nonempty single-line name", kind, id)
	}
	return nil
}

func validateEnv(env map[string]string) error {
	for key, value := range env {
		if !validEnv.MatchString(key) || strings.ContainsRune(value, 0) {
			return fmt.Errorf("invalid environment variable %q", key)
		}
	}
	return nil
}

func validPrefix(prefix string) bool {
	if prefix == "ctrl+space" {
		return true
	}
	if !strings.HasPrefix(prefix, "ctrl+") || len(prefix) != 6 {
		return false
	}
	c := prefix[5]
	return c >= 'a' && c <= 'z' || strings.ContainsRune("[\\]^_", rune(c))
}

// Return keys use the same canonical spelling as Bubble Tea KeyPressMsg.String.
// A sequence (for example :q) is never a single key event and is rejected.
func validReturnKey(key string) bool {
	if len([]rune(key)) == 1 {
		return !unicode.IsSpace([]rune(key)[0]) && !unicode.IsControl([]rune(key)[0])
	}
	switch key {
	case "esc", "enter", "tab", "shift+tab", "space", "backspace", "delete", "insert", "home", "end", "pgup", "pgdown", "up", "down", "left", "right":
		return true
	}
	if strings.HasPrefix(key, "f") {
		n, err := strconv.Atoi(key[1:])
		return err == nil && n >= 1 && n <= 12 && key == "f"+strconv.Itoa(n)
	}
	if strings.HasPrefix(key, "ctrl+") {
		return validPrefix(key)
	}
	return false
}
