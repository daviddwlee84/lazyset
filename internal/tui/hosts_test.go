package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"lazyset/internal/config"
	"lazyset/internal/core"
)

type hostFormService struct {
	fakeHostService
	probes int
}

func (s *hostFormService) Discover(context.Context, core.Host, []core.Tool) ([]core.Availability, error) {
	s.probes++
	return nil, nil
}

func hostFormModel(t *testing.T) (*Model, *hostFormService) {
	t.Helper()
	svc := &hostFormService{}
	m := New(config.Defaults(), filepath.Join(t.TempDir(), "config.toml"), Options{}, svc)
	cmd := m.action("host-add")
	if cmd == nil {
		t.Fatal("no host form preparation")
	}
	m.Update(cmd())
	if m.overlay != "hostedit" {
		t.Fatal("host form did not open")
	}
	return m, svc
}

func TestAddHostFormSavesFromFirstFieldWithoutConnecting(t *testing.T) {
	m, svc := hostFormModel(t)
	m.Update(tea.PasteMsg{Content: "user@gpu-box"})
	_, cmd := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("Ctrl+S from first field did not submit")
	}
	m.Update(cmd())
	if m.overlay != "hosts" || m.busy {
		t.Fatal("host save did not return to selector")
	}
	h, ok := m.cfg.Host("user-gpu-box")
	if !ok || h.SSH != "user@gpu-box" || h.Name != "user@gpu-box" {
		t.Fatalf("wrong saved host: %#v", h)
	}
	loaded, err := config.LoadSources(m.sources)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.Host(h.ID); !ok {
		t.Fatal("host was not persisted")
	}
	if svc.probes != 0 || len(svc.launches) != 0 || m.hostID != "local" {
		t.Fatal("saving unexpectedly connected or changed selected host")
	}
}

func TestHostFormTypingValidationAndCancellation(t *testing.T) {
	m, _ := hostFormModel(t)
	m.Update(tea.PasteMsg{Content: "bad alias"})
	if cmd := m.saveHost(); cmd != nil || m.busy || m.overlay != "hostedit" {
		t.Fatal("invalid alias submitted")
	}
	if _, err := os.Stat(m.path); !os.IsNotExist(err) {
		t.Fatal("invalid draft created config")
	}
	m.hostInputs[0].SetValue("gpu-box")
	m.focusHostField(1)
	for _, r := range "q/jk" {
		m.Update(char(r))
	}
	if m.hostInputs[1].Value() != "q/jk" || m.overlay != "hostedit" {
		t.Fatal("host name input triggered commands")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.overlay != "hosts" {
		t.Fatal("cancel did not return to Hosts")
	}
	if _, err := os.Stat(m.path); !os.IsNotExist(err) {
		t.Fatal("cancel created config")
	}
}

func TestHostFormConcurrentChangeRetainsDraft(t *testing.T) {
	m, _ := hostFormModel(t)
	m.hostInputs[0].SetValue("gpu-box")
	if err := os.WriteFile(m.sources.HostsPath, []byte("# edited elsewhere\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := m.saveHost()
	m.Update(cmd())
	if m.overlay != "hostedit" || m.hostInputs[0].Value() != "gpu-box" || m.busy {
		t.Fatal("save failure discarded host draft")
	}
	b, _ := os.ReadFile(m.sources.HostsPath)
	if !strings.Contains(string(b), "edited elsewhere") || strings.Contains(string(b), "gpu-box") {
		t.Fatal("concurrent file was overwritten")
	}
}

func TestShortHostFormKeepsFocusedFieldVisible(t *testing.T) {
	m, _ := hostFormModel(t)
	m.hostInputs[1].SetValue("Visible name")
	m.Update(tea.WindowSizeMsg{Width: 70, Height: 9})
	m.focusHostField(1)
	v := m.View()
	if !strings.Contains(v.Content, "Visible name") || v.Cursor != nil && v.Cursor.Y != 4 {
		t.Fatal("short form hid its focused field")
	}
	if m.hit(3, 4) != "host-field:1" {
		t.Fatal("short form mouse geometry disagrees with visible field")
	}
}

func TestHostSaveReconcilesChangedLaunchConfigWithoutProbing(t *testing.T) {
	m, svc := hostFormModel(t)
	m.availability["local"] = map[string]core.Availability{"btop": {ToolID: "btop", State: "found", Path: "/old/btop"}}
	updated := config.Defaults()
	updated.Tools[0].Command = []string{"different-btop"}
	updated.Hosts = append(updated.Hosts, core.Host{ID: "new", Name: "New", SSH: "alias"})
	m.Update(hostSavedMsg{ID: "new", Config: updated})
	if m.available("btop").State != "unknown" {
		t.Fatal("host save retained executable observation for changed tool")
	}
	if svc.probes != 0 {
		t.Fatal("host save initiated discovery")
	}
}
