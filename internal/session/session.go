// Package session runs an interactive program in an owned pseudo-terminal.
// The UI only reads cached frames; terminal I/O never runs on its event loop.
package session

import (
	"errors"
	"fmt"
	"image/color"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

const (
	maxPendingBytes  = 1 << 20
	maxPendingEvents = 128
	maxDimension     = 4096
)

var (
	ErrClosed    = errors.New("terminal session has exited")
	ErrInputFull = errors.New("terminal input queue is full")
)

type Spec struct {
	Command       []string
	Dir           string
	Env           map[string]string
	Width, Height int
}

// Frame is an immutable snapshot. Cursor coordinates are relative to its pane.
// ExitCode is -1 while running or when the process was terminated by a signal.
type Frame struct {
	Content       string
	Cursor        *tea.Cursor
	Width, Height int
	Exited        bool
	ExitCode      int
	Error         string
}

type input struct {
	message       tea.Msg
	literal       []byte
	width, height int
	bytes         int
}

// Session owns exactly one process, PTY and emulator. Input methods enqueue in
// caller order and return immediately; overload is reported instead of silently
// dropping input. Call Stop from an effect, since it waits for bounded cleanup.
type Session struct {
	cmd           *exec.Cmd
	ptmx          *os.File
	terminal      *vt.Emulator
	terminalInput io.Closer
	inputs        chan input
	output        chan []byte
	stopRequested chan struct{}
	stopIO        chan struct{}
	processDone   chan struct{}
	outputDrained chan struct{}
	done          chan struct{}
	stopOnce      sync.Once
	workers       sync.WaitGroup
	inputMu       sync.Mutex
	pendingBytes  int
	frameMu       sync.RWMutex
	frame         Frame
	stopErr       error
}

type terminalState struct {
	width, height                        int
	cursorVisible                        bool
	cursorShape                          tea.CursorShape
	cursorBlink                          bool
	cursorColor                          color.Color
	applicationCursor, applicationKeypad bool
	mouseModes                           map[ansi.Mode]bool
}

func Start(spec Spec) (*Session, error) {
	if len(spec.Command) == 0 || spec.Command[0] == "" {
		return nil, errors.New("terminal command is empty")
	}
	if err := validateSize(spec.Width, spec.Height); err != nil {
		return nil, err
	}
	cmd := exec.Command(spec.Command[0], spec.Command[1:]...)
	cmd.Dir = spec.Dir
	cmd.Env = environment(cmd.Environ(), spec.Env)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(spec.Width), Rows: uint16(spec.Height)})
	if err != nil {
		return nil, fmt.Errorf("start terminal: %w", err)
	}
	// Darwin's PTY is initially a blocking os.NewFile. Reopen a duplicate after
	// O_NONBLOCK is set so Go's poller can interrupt Read/Write during Close.
	ptmx, err = pollablePTY(ptmx)
	if err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return nil, fmt.Errorf("prepare terminal: %w", err)
	}
	s := &Session{
		cmd: cmd, ptmx: ptmx, terminal: vt.NewEmulator(spec.Width, spec.Height),
		inputs: make(chan input, maxPendingEvents), output: make(chan []byte, 16),
		stopRequested: make(chan struct{}), stopIO: make(chan struct{}),
		processDone: make(chan struct{}), outputDrained: make(chan struct{}), done: make(chan struct{}),
		frame: Frame{Width: spec.Width, Height: spec.Height, ExitCode: -1},
	}
	// InputPipe's public writer is an io.PipeWriter in the pinned VT version.
	// Closing the pipe first lets the response reader finish before VT.Close
	// touches its unsynchronized closed field.
	var ok bool
	s.terminalInput, ok = s.terminal.InputPipe().(io.Closer)
	if !ok {
		_ = ptmx.Close()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		_ = s.terminal.Close()
		return nil, errors.New("terminal emulator input cannot be closed")
	}
	state := terminalState{width: spec.Width, height: spec.Height, cursorVisible: true, cursorBlink: true, mouseModes: make(map[ansi.Mode]bool)}
	s.terminal.SetScrollbackSize(1000)
	s.terminal.SetCallbacks(vt.Callbacks{
		CursorVisibility: func(visible bool) { state.cursorVisible = visible },
		CursorStyle: func(shape vt.CursorStyle, steady bool) {
			// The pinned x/vt callback passes !blink despite its parameter name.
			state.cursorBlink = !steady
			switch shape {
			case vt.CursorBar:
				state.cursorShape = tea.CursorBar
			case vt.CursorUnderline:
				state.cursorShape = tea.CursorUnderline
			default:
				state.cursorShape = tea.CursorBlock
			}
		},
		CursorColor: func(c color.Color) { state.cursorColor = c },
		EnableMode:  func(m ansi.Mode) { state.setMode(m, true) },
		DisableMode: func(m ansi.Mode) { state.setMode(m, false) },
	})
	s.publish(&state)
	s.workers.Add(3)
	go s.readOutput()
	go s.writeInput()
	go s.run(&state)
	go s.waitProcess()
	go s.supervise()
	return s, nil
}

func pollablePTY(f *os.File) (*os.File, error) {
	fd, err := syscall.Dup(int(f.Fd()))
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	syscall.CloseOnExec(fd)
	if err = syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd)
		_ = f.Close()
		return nil, err
	}
	result := os.NewFile(uintptr(fd), f.Name())
	_ = f.Close()
	return result, nil
}

func environment(base []string, overrides map[string]string) []string {
	values := make(map[string]string, len(base)+len(overrides))
	for _, entry := range base {
		if k, v, ok := strings.Cut(entry, "="); ok {
			values[k] = v
		}
	}
	for k, v := range overrides {
		values[k] = v
	}
	values["TERM"] = "xterm-256color"
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, k := range keys {
		result = append(result, k+"="+values[k])
	}
	return result
}

func validateSize(width, height int) error {
	if width < 1 || height < 1 || width > maxDimension || height > maxDimension || width*height > 1<<20 {
		return fmt.Errorf("invalid terminal size %dx%d", width, height)
	}
	return nil
}

func (s *Session) Send(msg tea.Msg) error {
	switch m := msg.(type) {
	case tea.KeyPressMsg:
		return s.enqueue(input{message: m, bytes: len(m.Text) + 16})
	case tea.PasteMsg:
		return s.enqueue(input{message: m, bytes: len(m.Content) + 12})
	case tea.MouseClickMsg, tea.MouseReleaseMsg, tea.MouseMotionMsg, tea.MouseWheelMsg:
		return s.enqueue(input{message: m, bytes: 32})
	default:
		return nil
	}
}

func (s *Session) Write(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if len(data) > maxPendingBytes {
		return ErrInputFull
	}
	return s.enqueue(input{literal: append([]byte(nil), data...), bytes: len(data)})
}

func (s *Session) Resize(width, height int) error {
	if err := validateSize(width, height); err != nil {
		return err
	}
	return s.enqueue(input{width: width, height: height})
}

func (s *Session) enqueue(event input) error {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	select {
	case <-s.processDone:
		return ErrClosed
	case <-s.stopRequested:
		return ErrClosed
	default:
	}
	if s.pendingBytes+event.bytes > maxPendingBytes {
		return ErrInputFull
	}
	select {
	case s.inputs <- event:
		s.pendingBytes += event.bytes
		return nil
	default:
		return ErrInputFull
	}
}

func (s *Session) Snapshot() Frame {
	s.frameMu.RLock()
	defer s.frameMu.RUnlock()
	frame := s.frame
	if frame.Cursor != nil {
		c := *frame.Cursor
		frame.Cursor = &c
	}
	return frame
}

func (s *Session) Done() <-chan struct{} { return s.done }

func (s *Session) Stop() error {
	select {
	case <-s.done:
		return s.stopErr
	default:
	}
	s.stopOnce.Do(func() { close(s.stopRequested) })
	select {
	case <-s.done:
		return s.stopErr
	case <-time.After(3 * time.Second):
		return errors.New("terminal cleanup timed out")
	}
}

func (s *Session) readOutput() {
	defer s.workers.Done()
	defer close(s.output)
	buffer := make([]byte, 16*1024)
	for {
		n, err := s.ptmx.Read(buffer)
		if n > 0 {
			chunk := append([]byte(nil), buffer[:n]...)
			select {
			case s.output <- chunk:
			case <-s.stopIO:
				return
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, syscall.EIO) && !errors.Is(err, os.ErrClosed) {
				s.setError(err)
			}
			return
		}
	}
}

// All keyboard, paste, mouse and emulator replies leave through the same pipe,
// preserving order even when a child requests terminal capabilities mid-input.
func (s *Session) writeInput() {
	defer s.workers.Done()
	buffer := make([]byte, 16*1024)
	for {
		n, err := s.terminal.Read(buffer)
		if n > 0 {
			if _, writeErr := s.ptmx.Write(buffer[:n]); writeErr != nil {
				// Keep draining VT until shutdown: abandoning this reader can
				// leave the actor blocked in a capability response or paste.
				select {
				case <-s.stopIO:
					return
				default:
				}
				s.setError(writeErr)
				s.stopOnce.Do(func() { close(s.stopRequested) })
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *Session) run(state *terminalState) {
	defer s.workers.Done()
	output := s.output
	for {
		select {
		case <-s.stopIO:
			return
		case bytes, ok := <-output:
			if !ok {
				close(s.outputDrained)
				output = nil
				continue
			}
			_, _ = s.terminal.Write(bytes)
			// Coalesce a bounded batch while still servicing input regularly.
			for range 7 {
				select {
				case bytes, ok := <-output:
					if !ok {
						close(s.outputDrained)
						output = nil
					} else {
						_, _ = s.terminal.Write(bytes)
					}
				default:
				}
			}
			s.publish(state)
		case event := <-s.inputs:
			s.handleInput(event, state)
			s.inputMu.Lock()
			s.pendingBytes -= event.bytes
			s.inputMu.Unlock()
		}
	}
}

func (s *Session) handleInput(event input, state *terminalState) {
	if event.width > 0 {
		if err := pty.Setsize(s.ptmx, &pty.Winsize{Cols: uint16(event.width), Rows: uint16(event.height)}); err != nil {
			s.setError(err)
			return
		}
		s.terminal.Resize(event.width, event.height)
		state.width, state.height = event.width, event.height
		s.publish(state)
		return
	}
	if event.literal != nil {
		s.terminal.SendText(string(event.literal))
		return
	}
	switch msg := event.message.(type) {
	case tea.KeyPressMsg:
		if text := encodeKey(msg, state.applicationCursor, state.applicationKeypad); text != "" {
			s.terminal.SendText(text)
		}
	case tea.PasteMsg:
		s.terminal.Paste(msg.Content)
	case tea.MouseClickMsg:
		if state.acceptMouse(msg) {
			s.terminal.SendMouse(vt.MouseClick(msg))
		}
	case tea.MouseReleaseMsg:
		if state.acceptMouse(msg) {
			s.terminal.SendMouse(vt.MouseRelease(msg))
		}
	case tea.MouseMotionMsg:
		if state.acceptMouse(msg) {
			s.terminal.SendMouse(vt.MouseMotion(msg))
		}
	case tea.MouseWheelMsg:
		if state.acceptMouse(msg) {
			s.terminal.SendMouse(vt.MouseWheel(msg))
		}
	}
}

func (state *terminalState) setMode(mode ansi.Mode, enabled bool) {
	switch mode {
	case ansi.ModeCursorKeys:
		state.applicationCursor = enabled
	case ansi.ModeNumericKeypad:
		state.applicationKeypad = enabled
	case ansi.ModeMouseX10, ansi.ModeMouseNormal, ansi.ModeMouseButtonEvent, ansi.ModeMouseAnyEvent:
		state.mouseModes[mode] = enabled
	}
}

func (state *terminalState) acceptMouse(msg tea.MouseMsg) bool {
	m := msg.Mouse()
	if m.X < 0 || m.Y < 0 || m.X >= state.width || m.Y >= state.height {
		return false
	}
	switch msg.(type) {
	case tea.MouseMotionMsg:
		return state.mouseModes[ansi.ModeMouseAnyEvent] || (state.mouseModes[ansi.ModeMouseButtonEvent] && m.Button != tea.MouseNone)
	case tea.MouseReleaseMsg:
		return state.mouseModes[ansi.ModeMouseNormal] || state.mouseModes[ansi.ModeMouseButtonEvent] || state.mouseModes[ansi.ModeMouseAnyEvent]
	default:
		return state.mouseModes[ansi.ModeMouseX10] || state.mouseModes[ansi.ModeMouseNormal] || state.mouseModes[ansi.ModeMouseButtonEvent] || state.mouseModes[ansi.ModeMouseAnyEvent]
	}
}

func (s *Session) publish(state *terminalState) {
	content := s.terminal.Render()
	var cursor *tea.Cursor
	pos := s.terminal.CursorPosition()
	if state.cursorVisible && pos.X >= 0 && pos.Y >= 0 && pos.X < state.width && pos.Y < state.height {
		cursor = tea.NewCursor(pos.X, pos.Y)
		cursor.Shape, cursor.Blink, cursor.Color = state.cursorShape, state.cursorBlink, state.cursorColor
	}
	s.frameMu.Lock()
	s.frame.Content, s.frame.Cursor = content, cursor
	s.frame.Width, s.frame.Height = state.width, state.height
	s.frameMu.Unlock()
}

func (s *Session) setError(err error) {
	s.frameMu.Lock()
	if s.frame.Error == "" {
		s.frame.Error = err.Error()
	}
	s.frameMu.Unlock()
}

func (s *Session) waitProcess() {
	err := s.cmd.Wait()
	s.frameMu.Lock()
	s.frame.Exited = true
	s.frame.ExitCode = s.cmd.ProcessState.ExitCode()
	if err != nil && s.frame.Error == "" {
		s.frame.Error = err.Error()
	}
	s.frameMu.Unlock()
	close(s.processDone)
}

func (s *Session) supervise() {
	select {
	case <-s.processDone:
	case <-s.stopRequested:
		_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-s.processDone:
		case <-time.After(500 * time.Millisecond):
			_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
			select {
			case <-s.processDone:
			case <-time.After(time.Second):
				s.stopErr = errors.New("terminal process did not exit after SIGKILL")
			}
		}
	}
	// Give final child output a chance to reach its cached frame. A descendant
	// retaining the PTY cannot keep the application alive indefinitely.
	select {
	case <-s.outputDrained:
	case <-time.After(100 * time.Millisecond):
	}
	_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
	close(s.stopIO)
	_ = s.ptmx.Close()
	_ = s.terminalInput.Close()
	s.workers.Wait()
	// No Read or terminal mutation remains, so the pinned VT Close is safe.
	_ = s.terminal.Close()
	close(s.done)
}
