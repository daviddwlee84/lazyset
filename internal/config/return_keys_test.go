package config

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestReturnKeyOverridesAndLegacyQCompatibility(t *testing.T) {
	cases := []struct {
		name, id, fields string
		want             []string
	}{
		{"inherited q only", "lazygit", "", []string{"q"}},
		{"disable all", "lazygit", "return_keys=[]", []string{}},
		{"replace", "lazygit", "return_keys=['esc','f1','Q']", []string{"esc", "f1", "Q"}},
		{"dev explicit Esc override", "dev", "return_keys=['esc']", []string{"esc"}},
		{"dev explicit Ctrl+C override", "dev", "return_keys=['q','ctrl+c']", []string{"q", "ctrl+c"}},
		{"dev explicit empty override", "dev", "return_keys=[]", []string{}},
		{"legacy false removes q", "lazygit", "q_to_observe=false", []string{}},
		{"legacy true preserves q", "lazygit", "q_to_observe=true", []string{"q"}},
		{"translate inherited empty", "translate", "", nil},
		{"k9s inherited empty", "k9s", "", nil},
		{"legacy true adds q", "translate", "q_to_observe=true", []string{"q"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, "[[tools]]\nid='"+tc.id+"'\n"+tc.fields), true)
			if err != nil {
				t.Fatal(err)
			}
			tool, _ := cfg.Tool(tc.id)
			if !reflect.DeepEqual(tool.ReturnKeys, tc.want) {
				t.Fatalf("return keys = %v, want %v", tool.ReturnKeys, tc.want)
			}
			wantQ := false
			for _, key := range tc.want {
				wantQ = wantQ || key == "q"
			}
			if tool.QToObserve != wantQ {
				t.Fatal("legacy bool is not derived from effective keys")
			}
			builtin, _ := Defaults().Tool(tc.id)
			if !slices.Equal(tool.QuitKeys, builtin.QuitKeys) {
				t.Fatalf("override changed native quit metadata: %v, want %v", tool.QuitKeys, builtin.QuitKeys)
			}
		})
	}
	cfg, err := Load(writeConfig(t, "[[tools]]\nid='custom'\ncommand=['custom']"), true)
	if err != nil {
		t.Fatal(err)
	}
	tool, _ := cfg.Tool("custom")
	if len(tool.ReturnKeys) != 0 || tool.QToObserve {
		t.Fatal("custom commands inherited return keys")
	}
}

func TestRejectInvalidReturnKeysAndConflictingLegacySetting(t *testing.T) {
	for _, fields := range []string{
		"return_keys=[':q']", "return_keys=['q q']", "return_keys=['Ctrl+C']", "return_keys=['f13']",
		"return_keys=['q','q']", "return_keys=['']", "return_keys=['escape']",
		"return_keys=['q']\nq_to_observe=false",
	} {
		if _, err := Load(writeConfig(t, "[[tools]]\nid='btop'\n"+fields), true); err == nil || !strings.Contains(err.Error(), "return_keys") {
			t.Errorf("accepted %q: %v", fields, err)
		}
	}
}

func TestFocusClickDefaultAndValidation(t *testing.T) {
	if Defaults().FocusClick != "forward" {
		t.Fatal("default must forward the focus click")
	}
	cfg, err := Load(writeConfig(t, "focus_click='focus-only'"), true)
	if err != nil || cfg.FocusClick != "focus-only" {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
	if _, err := Load(writeConfig(t, "focus_click='invalid'"), true); err == nil || !strings.Contains(err.Error(), "focus_click") {
		t.Fatalf("got %v", err)
	}
}
