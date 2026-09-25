package host

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazyset/internal/core"
)

func TestLocalHomeDirectoryMatchesDiscoveryAndLaunch(t *testing.T) {
	for _, source := range []string{"inherited", "host", "tool"} {
		t.Run(source, func(t *testing.T) {
			base := t.TempDir()
			t.Chdir(base)
			home := filepath.Join(base, "home ' $(touch unexpected) `touch backtick`")
			suffix := "/files ' $(touch suffix)"
			dir := home + suffix
			if err := os.MkdirAll(filepath.Join(dir, "bin"), 0755); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(base, "tool-executed")
			want := writeExecutable(t, filepath.Join(dir, "bin"), "monitor", "touch "+shellQuote(marker))
			host := core.Host{ID: "local"}
			tool := core.Tool{ID: "monitor", Command: []string{"monitor"}, Dir: "~" + suffix, Env: map[string]string{"PATH": "bin"}}
			switch source {
			case "inherited":
				t.Setenv("HOME", home)
			case "host":
				host.Env = map[string]string{"HOME": home}
			case "tool":
				host.Env = map[string]string{"HOME": filepath.Join(base, "wrong-home")}
				tool.Env["HOME"] = home
			}
			manager := NewManager()
			t.Cleanup(func() { _ = manager.Close() })
			rows, err := manager.Discover(context.Background(), host, []core.Tool{tool})
			if err != nil || len(rows) != 1 || rows[0].State != "found" || rows[0].Path != want {
				t.Fatalf("discovery: %+v, %v", rows, err)
			}
			spec, err := manager.LaunchSpec(host, tool, rows[0].Path)
			if err != nil || spec.Dir != dir || spec.Command[0] != want {
				t.Fatalf("launch: %+v, %v", spec, err)
			}
			for _, path := range []string{marker, filepath.Join(base, "unexpected"), filepath.Join(base, "backtick"), filepath.Join(base, "suffix")} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("discovery executed text or tool: %s", path)
				}
			}
		})
	}
}

func TestLocalDirectoryOnlyExpandsHomeShorthand(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	home := filepath.Join(base, "configured home")
	for _, tc := range []struct{ input, want string }{
		{"~", home},
		{"~/files", filepath.Join(home, "files")},
		{"./~", filepath.Join(base, "~")},
		{"~someone", filepath.Join(base, "~someone")},
		{"$HOME", filepath.Join(base, "$HOME")},
		{"custom", filepath.Join(base, "custom")},
		{base, base},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := absoluteDir(tc.input, map[string]string{"HOME": home})
			if err != nil || got != tc.want {
				t.Fatalf("directory = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestRemoteHomeDirectoryMatchesDiscoveryAndLaunch(t *testing.T) {
	for _, shorthand := range []string{"~", "~/files ' $(touch suffix)"} {
		t.Run(shorthand, func(t *testing.T) {
			base := t.TempDir()
			t.Chdir(base)
			home := filepath.Join(base, "home ' $(touch unexpected) `touch backtick`")
			dir := home + strings.TrimPrefix(shorthand, "~")
			if err := os.MkdirAll(filepath.Join(dir, "bin"), 0755); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(base, "tool-executed")
			writeExecutable(t, filepath.Join(dir, "bin"), "monitor", "printf '%s\\000' \"$PWD\" \"$HOME\"; : > "+shellQuote(marker))
			manager, _ := fakeSSH(t, true)
			host := core.Host{ID: "remote", SSH: "alias", Env: map[string]string{"HOME": filepath.Join(base, "wrong-home")}}
			tool := core.Tool{ID: "monitor", Command: []string{"monitor"}, Dir: shorthand, Env: map[string]string{"HOME": home, "PATH": "bin"}}
			rows, err := manager.Discover(context.Background(), host, []core.Tool{tool})
			physical, resolveErr := filepath.EvalSymlinks(dir)
			if resolveErr != nil {
				t.Fatal(resolveErr)
			}
			if err != nil || len(rows) != 1 || rows[0].State != "found" {
				t.Fatalf("discovery: %+v, %v", rows, err)
			}
			found, resolveErr := filepath.EvalSymlinks(rows[0].Path)
			if resolveErr != nil || found != filepath.Join(physical, "bin", "monitor") {
				t.Fatalf("discovery path = %q, %v; want %q", rows[0].Path, resolveErr, filepath.Join(physical, "bin", "monitor"))
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("discovery executed tool")
			}
			spec, err := manager.LaunchSpec(host, tool, rows[0].Path)
			if err != nil {
				t.Fatal(err)
			}
			remote := spec.Command[len(spec.Command)-1]
			output, err := exec.Command("/bin/sh", "-c", remote).Output()
			if err != nil {
				t.Fatal(err)
			}
			fields := strings.Split(string(output), "\x00")
			if len(fields) != 3 || fields[0] != dir || fields[1] != home {
				t.Fatalf("unexpected cwd and HOME: %q", output)
			}
			for _, name := range []string{"unexpected", "backtick", "suffix"} {
				if _, err := os.Stat(filepath.Join(base, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("shell evaluated literal home text: %s", name)
				}
			}
		})
	}
}

func TestRemoteDirectoryUsesInheritedHomeAndPreservesLiteralDirectories(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	home := filepath.Join(base, "inherited home")
	t.Setenv("HOME", home)
	for _, tc := range []struct{ input, want string }{
		{"~", home},
		{"~/child", filepath.Join(home, "child")},
		{"./~", filepath.Join(base, "~")},
		{"~someone", filepath.Join(base, "~someone")},
		{"$HOME", filepath.Join(base, "$HOME")},
		{"custom", filepath.Join(base, "custom")},
		{base, base},
	} {
		t.Run(tc.input, func(t *testing.T) {
			if err := os.MkdirAll(tc.want, 0755); err != nil {
				t.Fatal(err)
			}
			remote, err := remoteInvocation([]string{"/bin/pwd", "-P"}, tc.input, nil)
			if err != nil {
				t.Fatal(err)
			}
			output, err := exec.Command("/bin/sh", "-c", remote).Output()
			want, resolveErr := filepath.EvalSymlinks(tc.want)
			if err != nil || resolveErr != nil || strings.TrimSpace(string(output)) != want {
				t.Fatalf("directory = %q, %v; want %q (%v)", output, err, want, resolveErr)
			}
		})
	}
}

func TestEmptyHomeDoesNotFallBackToRootOrStartupDirectory(t *testing.T) {
	tool := core.Tool{ID: "monitor", Command: []string{"/bin/pwd"}, Dir: "~", Env: map[string]string{"HOME": ""}}
	manager := NewManager()
	t.Cleanup(func() { _ = manager.Close() })
	rows, err := manager.Discover(context.Background(), core.Host{ID: "local"}, []core.Tool{tool})
	if err != nil || len(rows) != 1 || rows[0].State != "unknown" {
		t.Fatalf("discovery: %+v, %v", rows, err)
	}
	if _, err := manager.LaunchSpec(core.Host{ID: "local"}, tool, ""); err == nil {
		t.Fatal("local launch accepted empty HOME")
	}
	remoteManager, _ := fakeSSH(t, true)
	rows, err = remoteManager.Discover(context.Background(), core.Host{ID: "remote", SSH: "alias"}, []core.Tool{tool})
	if err != nil || len(rows) != 1 || rows[0].State != "unknown" {
		t.Fatalf("remote discovery: %+v, %v", rows, err)
	}
	remote, err := remoteInvocation(tool.Command, tool.Dir, tool.Env)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", remote)
	if err := cmd.Run(); err == nil || cmd.ProcessState.ExitCode() != 125 {
		t.Fatalf("remote launch accepted empty HOME: %v", err)
	}
}
