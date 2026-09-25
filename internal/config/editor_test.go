package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestEditorArgumentParsingDoesNotEvaluateShell(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{`nvim -f 'a b' "c d"`, []string{"nvim", "-f", "a b", "c d"}},
		{`'/path with spaces/editor' --wait`, []string{"/path with spaces/editor", "--wait"}},
		{`editor a\ b "" '$(touch /tmp/unwanted)'`, []string{"editor", "a b", "", "$(touch /tmp/unwanted)"}},
		{"editor \"`echo nope`\" '$HOME' * ;", []string{"editor", "`echo nope`", "$HOME", "*", ";"}},
		{`editor "a\qb" "a\"b"`, []string{"editor", `a\qb`, `a"b`}},
	}
	for _, tc := range cases {
		got, err := editorArgs(tc.input)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %#v, %v; want %#v", tc.input, got, err, tc.want)
		}
	}
	for _, input := range []string{"", "'' arg", "nvim 'oops", "nvim \\", "nvim\x00"} {
		if _, err := editorArgs(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}

func TestEditCommandPreservesMalformedFileAndUsesVisual(t *testing.T) {
	dir := t.TempDir()
	editor := filepath.Join(dir, "editor with spaces")
	if err := os.WriteFile(editor, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", "'"+editor+"' --wait 'argument with spaces'")
	t.Setenv("EDITOR", "does-not-exist")
	path := filepath.Join(dir, "broken.toml")
	broken := "[[this is malformed but must be repairable"
	if err := os.WriteFile(path, []byte(broken), 0600); err != nil {
		t.Fatal(err)
	}
	cmd, err := EditCommand(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{editor, "--wait", "argument with spaces", path}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("got %#v want %#v", cmd.Args, want)
	}
	data, _ := os.ReadFile(path)
	if string(data) != broken {
		t.Fatal("edit command replaced malformed user content")
	}
	if _, err := Load(path, true); err == nil {
		t.Fatal("malformed config unexpectedly loaded")
	}
}

func TestEditCommandCreatesValidDefaultOnlyAfterEditorResolution(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new", "config.toml")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "/nonexistent/lazyset-editor-test")
	if _, err := EditCommand(path); err == nil {
		t.Fatal("missing editor succeeded")
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("failed editor lookup created state")
	}
	editor := filepath.Join(dir, "editor")
	if err := os.WriteFile(editor, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", editor+" -f")
	cmd, err := EditCommand(path)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Args[1] != "-f" || cmd.Args[2] != path {
		t.Fatalf("wrong args: %#v", cmd.Args)
	}
	cfg, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Prefix != "ctrl+\\" {
		t.Fatalf("wrong initial prefix: %q", cfg.Prefix)
	}
}
