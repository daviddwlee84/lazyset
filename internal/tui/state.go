package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/daviddwlee84/lazyset/internal/catalog"
	"github.com/daviddwlee84/lazyset/internal/config"
	"github.com/daviddwlee84/lazyset/internal/core"
)

type history struct {
	Host, Set string
	Explore   bool
	Views     map[string]viewState
}

func digest(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}
func (m *Model) statePath() (string, error) {
	dir, err := config.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "selection-"+digest([]string{m.path, m.sources.HostsPath})+".json"), nil
}
func (m *Model) restoreState(opts Options) {
	path, err := m.statePath()
	if err != nil {
		return
	}
	var saved history
	if readJSON(path, &saved) != nil {
		// Read v0.1 history when the default sibling hosts file is used.
		defaultHosts, _ := config.HostsPath(m.path)
		if m.sources.HostsPath != defaultHosts {
			return
		}
		legacy := filepath.Join(filepath.Dir(path), "selection-"+digest(m.path)+".json")
		if readJSON(legacy, &saved) != nil {
			return
		}
	}
	explicitStartup := len(m.startup.Queue) > 0
	if opts.Host == "" && !explicitStartup {
		if _, ok := m.cfg.Host(saved.Host); ok {
			m.hostID = saved.Host
		}
	}
	if opts.Set == "" && !explicitStartup {
		if _, ok := m.cfg.Set(saved.Set); ok {
			m.setID = saved.Set
			m.explore = saved.Explore
		}
	}
	if m.setID == catalog.AllSetID {
		m.setID = m.browseSet()
		m.explore = true
	}
	if saved.Views != nil {
		m.views = saved.Views
	}
	v := m.views[m.contextKey()]
	m.selected = v.Selected
	m.filter = v.Filter
	m.scroll = v.Scroll
	m.visibility = allVisible()
	if v.Visibility != nil && !explicitStartup {
		m.visibility = *v.Visibility
	}
	if explicitStartup {
		m.filter = ""
		m.selected = ""
		m.scroll = 0
	}
	m.active = ""
	for key, v := range m.views {
		v.Active = ""
		m.views[key] = v
	}
	m.resetSelection()
}
func (m *Model) saveState() error {
	visibility := m.visibility
	m.views[m.contextKey()] = viewState{Selected: m.selected, Filter: m.filter, Scroll: m.scroll, Visibility: &visibility}
	path, err := m.statePath()
	if err != nil {
		return err
	}
	return atomicJSON(path, history{m.hostID, m.setID, m.explore, m.views})
}

type discoveryCache struct {
	At   time.Time
	Rows []core.Availability
}

func cachePath(h core.Host, tools []core.Tool) (string, error) {
	dir, err := config.CacheDir()
	if err != nil {
		return "", err
	}
	// Store only a digest of launch/environment settings, never their values.
	key := digest(struct {
		Host  core.Host
		Tools []core.Tool
	}{h, tools})
	return filepath.Join(dir, "discovery-"+key+".json"), nil
}
func readDiscoveryCache(h core.Host, tools []core.Tool) ([]core.Availability, time.Time) {
	path, err := cachePath(h, tools)
	if err != nil {
		return nil, time.Time{}
	}
	var c discoveryCache
	if readJSON(path, &c) != nil {
		return nil, time.Time{}
	}
	return c.Rows, c.At
}
func writeDiscoveryCache(h core.Host, tools []core.Tool, rows []core.Availability) error {
	path, err := cachePath(h, tools)
	if err != nil {
		return err
	}
	return atomicJSON(path, discoveryCache{time.Now(), rows})
}
func readJSON(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(v)
}
func atomicJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".lazyset-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
