package session

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"
)

// This subprocess is a real raw-mode child attached to the session PTY. Its
// byte assertions cover the entire input path, not just the encoder function.
func TestPTYHelperProcess(t *testing.T) {
	mode := os.Getenv("LAZYSET_SESSION_HELPER")
	if mode == "" {
		return
	}
	if _, err := term.MakeRaw(os.Stdin.Fd()); err != nil {
		os.Exit(3)
	}
	if mode == "blocked" {
		signal.Ignore(syscall.SIGTERM)
		fmt.Print("READY")
		for {
			time.Sleep(time.Hour)
		}
	}
	fmt.Print("READY" + os.Getenv("LAZYSET_TERMINAL_MODES"))
	if mode == "size" {
		b := make([]byte, 1)
		for {
			if _, err := io.ReadFull(os.Stdin, b); err != nil {
				os.Exit(4)
			}
			if b[0] == 'q' {
				os.Exit(0)
			}
			w, h, err := term.GetSize(os.Stdin.Fd())
			if err != nil {
				os.Exit(5)
			}
			fmt.Printf("\r\nSIZE:%dx%d", w, h)
		}
	}
	expected, err := hex.DecodeString(os.Getenv("LAZYSET_EXPECT_BYTES"))
	if err != nil {
		os.Exit(6)
	}
	got := make([]byte, len(expected))
	if _, err := io.ReadFull(os.Stdin, got); err != nil {
		os.Exit(7)
	}
	if string(got) != string(expected) {
		fmt.Printf("\r\nMISMATCH:%x", got)
		os.Exit(8)
	}
	fmt.Print("\r\nMATCH")
	os.Exit(0)
}

func helperSession(t *testing.T, mode, modes, expected string) *Session {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s, err := Start(Spec{
		Command: []string{executable, "-test.run=^TestPTYHelperProcess$"},
		Width:   80, Height: 24,
		Env: map[string]string{
			"LAZYSET_SESSION_HELPER": mode,
			"LAZYSET_TERMINAL_MODES": modes,
			"LAZYSET_EXPECT_BYTES":   hex.EncodeToString([]byte(expected)),
			// Race binaries otherwise linger for one second after os.Exit.
			"GORACE": "atexit_sleep_ms=0",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Stop(); err != nil {
			t.Error(err)
		}
	})
	waitFrame(t, s, func(f Frame) bool { return strings.Contains(f.Content, "READY") })
	return s
}

func waitFrame(t *testing.T, s *Session, predicate func(Frame) bool) Frame {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		frame := s.Snapshot()
		if predicate(frame) {
			return frame
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("terminal did not reach expected state: %+v", s.Snapshot())
	return Frame{}
}

func awaitDone(t *testing.T, s *Session) Frame {
	t.Helper()
	select {
	case <-s.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("terminal did not exit")
	}
	return s.Snapshot()
}

func TestPTYOrderedInputPasteMouseAndRelease(t *testing.T) {
	paste := "q\n中文🙂\x1c"
	expected := "q\x1b[200~" + paste + "\x1b[201~\x1bOA\x1c\x1b[<4;3;4M\x1b[<4;3;4m\x1b[<64;3;4M!"
	s := helperSession(t, "input", "\x1b[?1h\x1b[?2004h\x1b[?1000h\x1b[?1006h", expected)
	messages := []tea.Msg{
		tea.KeyPressMsg{Code: 'q', Text: "q"},
		tea.KeyReleaseMsg{Code: 'q', Text: "q"},
		tea.PasteMsg{Content: paste},
		tea.KeyPressMsg{Code: tea.KeyUp},
		tea.KeyPressMsg{Code: '\\', Mod: tea.ModCtrl},
		tea.MouseClickMsg{X: 2, Y: 3, Button: tea.MouseLeft, Mod: tea.ModShift},
		tea.MouseReleaseMsg{X: 2, Y: 3, Button: tea.MouseLeft, Mod: tea.ModShift},
		// Normal tracking must not accidentally send hover/drag events.
		tea.MouseMotionMsg{X: 2, Y: 3, Button: tea.MouseLeft},
		tea.MouseWheelMsg{X: 2, Y: 3, Button: tea.MouseWheelUp},
	}
	for _, msg := range messages {
		if err := s.Send(msg); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Write([]byte("!")); err != nil {
		t.Fatal(err)
	}
	frame := awaitDone(t, s)
	if frame.ExitCode != 0 || !strings.Contains(frame.Content, "MATCH") {
		t.Fatalf("input mismatch: %+v", frame)
	}
}

func TestPTYPasteWithoutBracketedMode(t *testing.T) {
	s := helperSession(t, "input", "", "q\nplain")
	if err := s.Send(tea.PasteMsg{Content: "q\nplain"}); err != nil {
		t.Fatal(err)
	}
	if frame := awaitDone(t, s); frame.ExitCode != 0 {
		t.Fatalf("unbracketed paste: %+v", frame)
	}
}

func TestPTYCapabilityResponse(t *testing.T) {
	// READY occupies five cells. The child asks for its cursor position before
	// accepting normal input; a missing response pump would freeze here.
	s := helperSession(t, "input", "\x1b[6n", "\x1b[1;6R")
	if frame := awaitDone(t, s); frame.ExitCode != 0 {
		t.Fatalf("DSR reply: %+v", frame)
	}
}

func TestPTYResizeCursorAndSnapshotIsolation(t *testing.T) {
	s := helperSession(t, "size", "\x1b[3;7H\x1b[6 q", "")
	frame := waitFrame(t, s, func(f Frame) bool { return f.Cursor != nil && f.Cursor.X == 6 && f.Cursor.Y == 2 })
	if frame.Cursor.Shape != tea.CursorBar || frame.Cursor.Blink {
		t.Fatalf("cursor style: %+v", frame.Cursor)
	}
	frame.Cursor.X = 999
	if s.Snapshot().Cursor.X == 999 {
		t.Fatal("snapshot cursor aliases internal state")
	}
	if err := s.Resize(101, 31); err != nil {
		t.Fatal(err)
	}
	if err := s.Write([]byte("s")); err != nil {
		t.Fatal(err)
	}
	frame = waitFrame(t, s, func(f Frame) bool { return strings.Contains(f.Content, "SIZE:101x31") })
	if frame.Width != 101 || frame.Height != 31 {
		t.Fatalf("frame size: %+v", frame)
	}
	if err := s.Resize(0, 0); err == nil {
		t.Fatal("zero size accepted")
	}
	if err := s.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	awaitDone(t, s)
}

func TestBlockedChildKeepsInputAndSnapshotsResponsive(t *testing.T) {
	s := helperSession(t, "blocked", "", "")
	start := time.Now()
	chunk := []byte(strings.Repeat("x", maxPendingBytes/2))
	if err := s.Write(chunk); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(chunk); err != nil {
		t.Fatal(err)
	}
	if err := s.Write([]byte("overflow")); !errors.Is(err, ErrInputFull) {
		t.Fatalf("queue overflow: %v", err)
	}
	if !strings.Contains(s.Snapshot().Content, "READY") {
		t.Fatal("lost snapshot")
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("enqueue or snapshot blocked on child")
	}
	start = time.Now()
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2500*time.Millisecond {
		t.Fatal("Stop exceeded its cleanup bound")
	}
	if err := syscall.Kill(s.cmd.Process.Pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("child still exists: %v", err)
	}
	if err := s.Write([]byte("x")); !errors.Is(err, ErrClosed) {
		t.Fatalf("input after exit: %v", err)
	}
}

func TestExitCodeAndRepeatedSessionCleanup(t *testing.T) {
	before := runtime.NumGoroutine()
	for range 12 {
		s, err := Start(Spec{Command: []string{"/bin/sh", "-c", "printf final-frame; exit 23"}, Width: 80, Height: 24})
		if err != nil {
			t.Fatal(err)
		}
		frame := awaitDone(t, s)
		if !frame.Exited || frame.ExitCode != 23 || !strings.Contains(frame.Content, "final-frame") {
			t.Fatalf("exit result: %+v", frame)
		}
		if err := s.Stop(); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && runtime.NumGoroutine() > before+1 {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before+1 {
		t.Fatalf("goroutines accumulated: before %d, after %d", before, after)
	}
}

func TestStartValidation(t *testing.T) {
	for _, spec := range []Spec{{Width: 80, Height: 24}, {Command: []string{"/bin/sh"}, Width: 0, Height: 24}, {Command: []string{"/this-command-does-not-exist"}, Width: 80, Height: 24}} {
		if s, err := Start(spec); err == nil {
			_ = s.Stop()
			t.Fatalf("unexpected successful Start: %+v", spec)
		}
	}
}
