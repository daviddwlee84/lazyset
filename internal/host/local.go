package host

import (
	"context"
	"debug/buildinfo"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/daviddwlee84/lazyset/internal/core"
)

func discoverLocal(ctx context.Context, h core.Host, tools []core.Tool) ([]core.Availability, error) {
	rows := make([]core.Availability, 0, len(tools))
	for _, t := range tools {
		if err := ctx.Err(); err != nil {
			return append(rows, unknown(tools[len(rows):], err.Error())...), err
		}
		row := core.Availability{ToolID: t.ID, State: "missing", Reason: "executable is not on PATH"}
		if !supported(t, runtime.GOOS) {
			row.State, row.Reason = "unsupported", "not supported on "+runtime.GOOS
			rows = append(rows, row)
			continue
		}
		env := mergedEnv(h.Env, t.Env)
		if err := validateInvocation(t.Command, t.Dir, env); err != nil {
			row.State, row.Reason = "unknown", err.Error()
			rows = append(rows, row)
			continue
		}
		dir, err := absoluteDir(t.Dir, env)
		if err != nil {
			row.State, row.Reason = "unknown", err.Error()
			rows = append(rows, row)
			continue
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			row.State, row.Reason = "unknown", "working directory is unavailable"
			rows = append(rows, row)
			continue
		}
		pathValue := os.Getenv("PATH")
		if value, ok := env["PATH"]; ok {
			pathValue = value
		}
		for _, name := range candidates(t) {
			path, err := findExecutable(name, pathValue, dir)
			if err != nil {
				continue
			}
			if t.ID == "gdu" && filepath.Base(path) == "gdu" {
				ok, reason := identifyGDU(ctx, path, dir, env)
				if !ok {
					row.Reason = reason
					if !strings.Contains(reason, "coreutils") {
						row.State = "unknown"
					}
					continue
				}
			}
			row.State, row.Path, row.Reason = "found", path, ""
			break
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func findExecutable(name, pathValue, dir string) (string, error) {
	if name == "" || strings.ContainsRune(name, 0) {
		return "", exec.ErrNotFound
	}
	check := func(path string) (string, error) {
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return "", os.ErrPermission
		}
		return filepath.Clean(path), nil
	}
	if strings.ContainsRune(name, filepath.Separator) {
		return check(name)
	}
	for _, entry := range filepath.SplitList(pathValue) {
		if entry == "" {
			entry = "."
		}
		if path, err := check(filepath.Join(entry, name)); err == nil {
			return path, nil
		}
	}
	return "", exec.ErrNotFound
}

func identifyGDU(ctx context.Context, path, dir string, env map[string]string) (bool, string) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		slash := filepath.ToSlash(resolved)
		if strings.Contains(slash, "/coreutils/") {
			return false, "gdu belongs to GNU coreutils, not the disk-usage TUI"
		}
		if strings.Contains(slash, "/Cellar/gdu/") || strings.Contains(slash, "/opt/gdu/") {
			return true, ""
		}
		if info, err := buildinfo.ReadFile(resolved); err == nil && strings.HasPrefix(info.Main.Path, "github.com/dundee/gdu") {
			return true, ""
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, path, "--version")
	cmd.WaitDelay = 200 * time.Millisecond
	cmd.Dir, cmd.Env = dir, envSlice(env)
	var output limitedBuffer
	cmd.Stdout, cmd.Stderr = &output, &output
	err = cmd.Run()
	text := output.String()
	if strings.Contains(strings.ToLower(text), "coreutils") {
		return false, "gdu belongs to GNU coreutils, not the disk-usage TUI"
	}
	if err == nil && strings.Contains(text, "Version:") && strings.Contains(text, "Built time:") && strings.Contains(text, "Built user:") {
		return true, ""
	}
	if errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
		return false, "gdu identity probe timed out"
	}
	return false, fmt.Sprintf("cannot verify %s is the gdu disk-usage TUI", path)
}
