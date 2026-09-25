#!/usr/bin/env python3
"""Exercise a built lazyset in a real PTY using only Python's standard library.

    go build -o bin/lazyset ./cmd/lazyset
    python3 scripts/pty_smoke.py bin/lazyset --real-monitors --real-lazychezmoi --real-dev --real-superfile

The fixture checks actual child input, PIDs, window sizes and cleanup. Its small
screen reader locates ASCII UI labels; it is not a Unicode visual-layout test.
A fake SSH destination is saved to test the host form; a disposable SSH stub
ensures no destination is contacted. All writes use temporary XDG directories
and a temporary working directory. The optional Superfile check also gives its
subprocess a disposable HOME; the invoking shell's HOME is never changed.
"""

from __future__ import annotations

import argparse
import codecs
import errno
import fcntl
import json
import os
from pathlib import Path
import pty
import select
import shutil
import signal
import struct
import subprocess
import sys
import tempfile
import termios
import time
import unicodedata


PREFIX = b"\x1c"  # Ctrl+\, the configured default.
PASTE_BEGIN, PASTE_END = b"\x1b[200~", b"\x1b[201~"


FIXTURE = r'''
import json, os, select, signal, sys, termios, tty
from pathlib import Path

tag, raw_path, state_path = sys.argv[1:]
raw = os.open(raw_path, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
state_path = Path(state_path)
original = termios.tcgetattr(0)
count = 0
dirty = True

def changed(*_):
    global dirty
    dirty = True

def finish(*_):
    raise SystemExit(0)

signal.signal(signal.SIGWINCH, changed)
signal.signal(signal.SIGTERM, finish)
signal.signal(signal.SIGHUP, finish)
tty.setraw(0)
os.write(1, b'\x1b[?2004h\x1b[?1000h\x1b[?1006h')
try:
    while True:
        if dirty:
            columns, rows = os.get_terminal_size(0)
            state = dict(tag=tag, pid=os.getpid(), count=count,
                         columns=columns, rows=rows)
            temporary = state_path.with_suffix('.next')
            temporary.write_text(json.dumps(state))
            temporary.replace(state_path)
            frame = ('\x1b[2J\x1b[H' + 'FIXTURE:' + tag +
                     ' PID=' + str(os.getpid()) + ' COUNT=' + str(count) +
                     '\r\nSIZE=' + str(columns) + 'x' + str(rows) +
                     '\r\nUnicode bytes: 中文 e\u0301 😀\r\n')
            os.write(1, frame.encode())
            dirty = False
        readable, _, _ = select.select([0], [], [], 0.05)
        if readable:
            data = os.read(0, 65536)
            if not data:
                break
            os.write(raw, data)
            count += len(data)
            dirty = True
finally:
    os.close(raw)
    termios.tcsetattr(0, termios.TCSANOW, original)
'''


NATIVE_EDITOR = r'''
import json, os, re, sys, termios
from pathlib import Path

root = Path(os.environ['LAZYSET_PTY_HANDOFF_ROOT'])
mode = (root / 'editor-mode.txt').read_text().strip()
config = Path(sys.argv[1])
flags = termios.tcgetattr(0)[3]
record = dict(mode=mode, path=str(config), pid=os.getpid(), ready=True,
              stdin_tty=os.isatty(0), stdout_tty=os.isatty(1),
              canonical=bool(flags & termios.ICANON), echo=bool(flags & termios.ECHO))
(root / 'editor.json').write_text(json.dumps(record))
print('NATIVE_EDITOR_READY:' + mode, flush=True)
record['input'] = sys.stdin.readline()
if mode == 'invalid':
    config.write_text('prefix = [unterminated\n')
elif mode == 'repair':
    config.write_text((root / 'editor-valid.toml').read_text() + '# pty editor repaired\n')
elif mode == 'hosts':
    config.write_text(config.read_text() + '# pty hosts editor\n')
else:
    content = re.sub(r'(?m)^prefix\s*=.*$', 'prefix = "ctrl+g"', config.read_text(), count=1)
    config.write_text(content + '# pty editor valid\n')
    (root / 'editor-valid.toml').write_text(config.read_text())
record['saved'] = True
(root / 'editor.json').write_text(json.dumps(record))
print('NATIVE_EDITOR_SAVED:' + mode, flush=True)
'''


NATIVE_EXTERNAL = r'''
import json, os, sys, termios, tty
from pathlib import Path

root = Path(sys.argv[1])
original = termios.tcgetattr(0)
columns, rows = os.get_terminal_size(0)
record = dict(pid=os.getpid(), ready=True, columns=columns, rows=rows,
              stdin_tty=os.isatty(0), stdout_tty=os.isatty(1),
              canonical=bool(original[3] & termios.ICANON),
              echo=bool(original[3] & termios.ECHO))
(root / 'external.json').write_text(json.dumps(record))
tty.setraw(0)
try:
    os.write(1, b'\r\nNATIVE_EXTERNAL_READY\r\n')
    data = b''
    while b'q' not in data:
        data += os.read(0, 1024)
    (root / 'external.raw').write_bytes(data)
finally:
    termios.tcsetattr(0, termios.TCSANOW, original)
'''


class Screen:
    """Enough ANSI state for current ASCII labels, not a full terminal emulator."""

    def __init__(self, columns: int, rows: int):
        self.decoder = codecs.getincrementaldecoder("utf-8")("replace")
        self.state = "text"
        self.sequence = ""
        self.modes: dict[int, bool] = {}
        self.replies: list[bytes] = []
        self.saved = (0, 0)
        self.resize(columns, rows)

    def resize(self, columns: int, rows: int):
        self.columns, self.rows = columns, rows
        self.grid = [[" "] * columns for _ in range(rows)]
        self.x = self.y = 0

    def text(self) -> str:
        return "\n".join("".join(row) for row in self.grid)

    def newline(self):
        self.y += 1
        if self.y >= self.rows:
            self.grid.pop(0)
            self.grid.append([" "] * self.columns)
            self.y = self.rows - 1

    def feed(self, data: bytes):
        for char in self.decoder.decode(data):
            if self.state == "csi":
                if "@" <= char <= "~":
                    self.csi(self.sequence, char)
                    self.state, self.sequence = "text", ""
                else:
                    self.sequence += char
                continue
            if self.state in ("osc", "dcs"):
                if char == "\x07":
                    self.string_end()
                elif char == "\x1b":
                    self.state += "_escape"
                else:
                    self.sequence += char
                continue
            if self.state.endswith("_escape"):
                if char == "\\":
                    self.string_end()
                else:
                    self.state = self.state.removesuffix("_escape")
                    self.sequence += char
                continue
            if self.state == "escape":
                if char == "[":
                    self.state, self.sequence = "csi", ""
                elif char in "]P":
                    self.state, self.sequence = ("osc" if char == "]" else "dcs"), ""
                elif char in "()*+":
                    self.state = "charset"
                else:
                    if char == "7":
                        self.saved = self.x, self.y
                    elif char == "8":
                        self.x, self.y = self.saved
                    elif char == "D":
                        self.newline()
                    elif char == "M":
                        self.y = max(0, self.y - 1)
                    self.state = "text"
                continue
            if self.state == "charset":
                self.state = "text"
                continue
            if char == "\x1b":
                self.state = "escape"
            elif char == "\r":
                self.x = 0
            elif char in "\n\v\f":
                self.newline()
            elif char == "\b":
                self.x = max(0, self.x - 1)
            elif char == "\t":
                self.x = min(self.columns - 1, (self.x // 8 + 1) * 8)
            elif ord(char) >= 32 and char != "\x7f":
                if unicodedata.combining(char):
                    if self.x:
                        self.grid[self.y][min(self.x - 1, self.columns - 1)] += char
                    continue
                width = 2 if unicodedata.east_asian_width(char) in "WF" else 1
                if self.x + width > self.columns:
                    self.x = 0
                    self.newline()
                self.grid[self.y][self.x] = char
                if width == 2:
                    self.grid[self.y][self.x + 1] = ""
                self.x += width

    def string_end(self):
        if self.sequence in ("10;?", "11;?"):
            which = self.sequence[:2]
            color = "ffff/ffff/ffff" if which == "10" else "0000/0000/0000"
            self.replies.append(f"\x1b]{which};rgb:{color}\x1b\\".encode())
        self.state, self.sequence = "text", ""

    def csi(self, raw: str, final: str):
        private = raw.startswith("?")
        clean = raw.lstrip("?<>=").rstrip("$ ")
        try:
            values = [int(v or "0") for v in clean.split(";")]
        except ValueError:
            values = [0]
        first = values[0] or 1
        if final in "Hf":
            self.y = max(0, min(self.rows - 1, first - 1))
            self.x = max(0, min(self.columns - 1, (values[1] or 1) - 1 if len(values) > 1 else 0))
        elif final == "A":
            self.y = max(0, self.y - first)
        elif final in "Be":
            self.y = min(self.rows - 1, self.y + first)
        elif final in "Ca":
            self.x = min(self.columns - 1, self.x + first)
        elif final == "D":
            self.x = max(0, self.x - first)
        elif final in "EF":
            self.y = max(0, min(self.rows - 1, self.y + (first if final == "E" else -first)))
            self.x = 0
        elif final == "G":
            self.x = min(self.columns - 1, first - 1)
        elif final == "d":
            self.y = min(self.rows - 1, first - 1)
        elif final == "J":
            if values[0] in (2, 3):
                self.grid = [[" "] * self.columns for _ in range(self.rows)]
            elif values[0] == 0:
                self.grid[self.y][self.x:] = [" "] * (self.columns - self.x)
                for row in range(self.y + 1, self.rows):
                    self.grid[row] = [" "] * self.columns
        elif final == "K":
            start, end = (0, self.columns) if values[0] == 2 else ((0, self.x + 1) if values[0] == 1 else (self.x, self.columns))
            self.grid[self.y][start:end] = [" "] * (end - start)
        elif final == "X":
            end = min(self.columns, self.x + first)
            self.grid[self.y][self.x:end] = [" "] * (end - self.x)
        elif final in "hl" and private:
            for mode in values:
                self.modes[mode] = final == "h"
        elif final == "n" and values[0] == 6:
            self.replies.append(f"\x1b[{self.y + 1};{min(self.x + 1, self.columns)}R".encode())
        elif final == "c":
            self.replies.append(b"\x1b[>0;136;0c" if raw.startswith(">") else b"\x1b[?1;2c")
        elif final == "u" and raw == "?":
            self.replies.append(b"\x1b[?3u")
        elif final == "p" and raw == "?2026$":
            self.replies.append(b"\x1b[?2026;2$y")


class Driver:
    def __init__(self, binary: Path, root: Path, config: Path, tool: str | None, env: dict[str, str],
                 args: list[str] | None = None):
        self.master, self.slave = pty.openpty()
        self.screen = Screen(120, 36)
        self.transcript = bytearray()
        self.children: set[int] = set()
        self.root = root
        self.prefix = PREFIX
        fcntl.ioctl(self.slave, termios.TIOCSWINSZ, struct.pack("HHHH", 36, 120, 0, 0))
        self.original_termios = termios.tcgetattr(self.slave)

        def own_tty():
            os.setsid()
            fcntl.ioctl(0, termios.TIOCSCTTY, 0)

        command = [str(binary), "--config", str(config), "--set", "testset"]
        if tool is not None:
            command.extend(["--tool", tool])
        command.extend(args or [])
        self.process = subprocess.Popen(
            command,
            stdin=self.slave, stdout=self.slave, stderr=self.slave, cwd=root,
            env=env, preexec_fn=own_tty, close_fds=True,
        )
        os.set_blocking(self.master, False)

    def pump(self, timeout: float = 0.05):
        if select.select([self.master], [], [], timeout)[0]:
            try:
                data = os.read(self.master, 65536)
            except OSError as error:
                if error.errno in (errno.EIO, errno.EAGAIN):
                    return
                raise
            self.transcript.extend(data)
            self.screen.feed(data)
            for reply in self.screen.replies:
                self.send(reply)
            self.screen.replies.clear()

    def send(self, data: bytes):
        view = memoryview(data)
        while view:
            try:
                count = os.write(self.master, view)
                view = view[count:]
            except BlockingIOError:
                select.select([], [self.master], [], 0.1)

    def wait(self, condition, description: str, timeout: float = 8):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            self.pump()
            if condition():
                return
            if self.process.poll() is not None:
                raise AssertionError(f"app exited ({self.process.returncode}) while waiting for {description}")
        raise AssertionError(f"timeout waiting for {description}\n{self.screen.text()}")

    def quiet(self, duration: float = 0.15):
        deadline = time.monotonic() + duration
        while time.monotonic() < deadline:
            self.pump(max(0.0, min(0.03, deadline - time.monotonic())))

    def mode(self, mode: str):
        self.wait(lambda: f"[{mode}]" in "".join(self.screen.grid[0]), mode)

    def marker(self, tag: str):
        self.wait(lambda: f"FIXTURE:{tag} PID=" in self.screen.text() and "Confirm: " not in self.screen.text(),
                  f"visible fixture {tag} without a modal")

    def click_header(self, label: str):
        row = "".join(self.screen.grid[1])
        x = row.find(f"[{label}]")
        if x < 0:
            raise AssertionError(f"header {label} absent: {row!r}")
        self.send(f"\x1b[<0;{x + 2};2M\x1b[<0;{x + 2};2m".encode())

    def click_quit(self):
        row = "".join(self.screen.grid[0])
        x = row.find("[Quit]")
        if x < 0 or x + len("[Quit]") != self.screen.columns:
            raise AssertionError(f"top-right Quit absent: {row!r}")
        self.click(x + 2, 0)

    def click(self, x: int, y: int):
        """Send a complete left click at zero-based outer-terminal cells."""
        self.send(f"\x1b[<0;{x + 1};{y + 1}M\x1b[<0;{x + 1};{y + 1}m".encode())

    def click_text(self, label: str):
        # A title can arrive in an earlier PTY read than its popup buttons.
        self.wait(lambda: any(label in "".join(row) for row in self.screen.grid), f"clickable label {label!r}")
        for y, row in enumerate(self.screen.grid):
            x = "".join(row).find(label)
            if x >= 0:
                self.click(x + 1, y)
                return
        raise AssertionError(f"label absent for click: {label!r}\n{self.screen.text()}")

    def sidebar_text(self) -> str:
        return "\n".join("".join(row[1:28]) for row in self.screen.grid[4:-4])

    def resize(self, columns: int, rows: int):
        self.screen.resize(columns, rows)
        fcntl.ioctl(self.slave, termios.TIOCSWINSZ, struct.pack("HHHH", rows, columns, 0, 0))
        os.kill(self.process.pid, signal.SIGWINCH)

    def quit(self, command: bool = False, mouse: bool = False):
        if mouse:
            self.click_quit()
        else:
            self.send(b":q\r" if command else self.prefix + b"Q")
        self.wait(lambda: "Confirm: quit" in self.screen.text(), "explicit quit confirmation")
        if mouse:
            self.click_text("[ Confirm ]")
        else:
            self.send(b"\x1b[C\r")
        deadline = time.monotonic() + 8
        while self.process.poll() is None and time.monotonic() < deadline:
            self.pump()
        if self.process.poll() is None:
            raise AssertionError("app did not finish its confirmed quit")
        self.quiet(0.1)
        if self.process.returncode != 0:
            raise AssertionError(f"app quit with status {self.process.returncode}")
        # Darwin revokes the slave when its controlling session leader exits.
        # The retained PTY master still exposes the terminal attributes.
        if termios.tcgetattr(self.master) != self.original_termios:
            raise AssertionError("outer terminal attributes were not restored")
        if self.screen.modes.get(1049, False) or not self.screen.modes.get(25, True):
            raise AssertionError("alternate screen or cursor was not restored")
        if any(self.screen.modes.get(mode, False) for mode in (1000, 1002, 1003, 1006, 2004)):
            raise AssertionError("mouse or bracketed-paste mode remained enabled after quit")
        for pid in self.children:
            deadline = time.monotonic() + 3
            while alive(pid) and time.monotonic() < deadline:
                time.sleep(0.02)
            if alive(pid):
                raise AssertionError(f"owned child {pid} survived app exit")

    def close(self):
        (self.root / "transcript.bin").write_bytes(self.transcript)
        (self.root / "screen.txt").write_text(self.screen.text())
        if self.process.poll() is None:
            self.process.terminate()
            deadline = time.monotonic() + 2
            while self.process.poll() is None and time.monotonic() < deadline:
                self.pump()
            if self.process.poll() is None:
                self.process.kill()
                self.process.wait(timeout=2)
        for pid in self.children:
            if alive(pid):
                try:
                    os.killpg(pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                except PermissionError:
                    try:
                        os.kill(pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
        os.close(self.master)
        os.close(self.slave)


def alive(pid: int) -> bool:
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False


def state(root: Path, tag: str) -> dict:
    try:
        return json.loads((root / f"{tag}.json").read_text())
    except (FileNotFoundError, json.JSONDecodeError):
        return {}


def raw(root: Path, tag: str) -> bytes:
    path = root / f"{tag}.raw"
    return path.read_bytes() if path.exists() else b""


def environment(root: Path) -> dict[str, str]:
    result = os.environ.copy()
    result.update(TERM="xterm-256color", COLORTERM="truecolor", NO_COLOR="1", LC_ALL="C")
    for variable, name in (("XDG_CONFIG_HOME", "config"), ("XDG_DATA_HOME", "data"), ("XDG_STATE_HOME", "state"),
                           ("XDG_CACHE_HOME", "cache"), ("XDG_RUNTIME_DIR", "runtime")):
        path = root / name
        path.mkdir(mode=0o700, parents=True, exist_ok=True)
        result[variable] = str(path)
    result["HTOPRC"] = str(root / "config" / "htoprc")
    return result


def config_file(root: Path, tool_ids: list[str], focus_click: str | None = None) -> Path:
    fixture = root / "fixture.py"
    fixture.write_text(FIXTURE)
    lines = ['prefix = "ctrl+\\\\"', "mouse = true", 'default_set = "testset"']
    if focus_click:
        lines.append("focus_click = " + json.dumps(focus_click))
    for tag, keys in (("fixture", ["q", "ctrl+c", "esc"]),
                      ("fixture-pass", []), ("fixture-available", [])):
        command = [sys.executable, "-u", str(fixture), tag,
                   str(root / f"{tag}.raw"), str(root / f"{tag}.json")]
        lines.extend(["", "[[tools]]", "id = " + json.dumps(tag),
                      "name = " + json.dumps(tag),
                      "description = \"PTY input and lifecycle fixture\"",
                      "command = " + json.dumps(command),
                      "return_keys = " + json.dumps(keys)])
    lines.extend(["", "[[tools]]", 'id = "fixture-missing"',
                  'name = "fixture-missing"',
                  'command = ["lazyset-pty-definitely-not-installed"]'])
    lines.extend(["", "[[sets]]", 'id = "testset"', 'name = "PTY testset"',
                  "tools = " + json.dumps(tool_ids)])
    path = root / "config.toml"
    path.write_text("\n".join(lines) + "\n")
    return path


def fixture_smoke(binary: Path, root: Path):
    root.mkdir()
    config = config_file(root, ["fixture", "fixture-pass"])
    env = environment(root)
    ssh_stub_dir = root / "ssh-stub"
    ssh_stub_dir.mkdir()
    ssh_called = root / "ssh-called.txt"
    ssh_stub = ssh_stub_dir / "ssh"
    ssh_stub.write_text(f"#!{sys.executable}\nfrom pathlib import Path\n"
                        f"Path({str(ssh_called)!r}).write_text('unexpected SSH invocation')\n"
                        "raise SystemExit(99)\n")
    ssh_stub.chmod(0o700)
    env["PATH"] = str(ssh_stub_dir) + os.pathsep + env.get("PATH", "")
    driver = Driver(binary, root, config, "fixture", env)
    try:
        driver.marker("fixture")
        driver.mode("OBSERVE")
        driver.wait(lambda: bool(state(root, "fixture")), "fixture PID")
        first_pid = state(root, "fixture")["pid"]
        driver.children.add(first_pid)
        driver.send(b"\r")
        driver.mode("INTERACT")
        driver.send(b"q")
        driver.mode("OBSERVE")
        driver.quiet()
        assert raw(root, "fixture") == b"", "q guard sent input to the child"

        driver.send(b"\x1b[13;1:2u")  # Kitty repeated Enter, not a fresh press.
        driver.quiet()
        driver.mode("OBSERVE")
        assert raw(root, "fixture") == b"", "repeat Enter activated or wrote to the child"
        driver.send(b"\r")
        driver.mode("INTERACT")
        driver.send(PREFIX + b"q" + PREFIX + PREFIX)
        expected = b"q" + PREFIX
        driver.wait(lambda: raw(root, "fixture") == expected, "literal q and literal prefix")
        for guarded in (b"\x03", b"\x1b"):
            driver.send(guarded)
            driver.mode("OBSERVE")
            driver.quiet()
            assert raw(root, "fixture") == expected, "configured return key reached the child"
            driver.send(b"\r")
            driver.mode("INTERACT")
            driver.send(PREFIX + b"v" + guarded)
            expected += guarded
            driver.wait(lambda: raw(root, "fixture") == expected, "prefix-v literal guarded key")
        payload = "q\nline two\nq 中文 e\u0301 😀".encode()
        driver.send(PASTE_BEGIN + payload + PASTE_END)
        expected += PASTE_BEGIN + payload + PASTE_END
        driver.wait(lambda: raw(root, "fixture") == expected, "intact bracketed paste bytes")
        driver.mode("INTERACT")
        print("PASS return_keys q/Ctrl+C/Esc, Enter repeat, prefix-v/prefix/q and paste bytes", flush=True)

        driver.send(PREFIX + b"n")
        driver.marker("fixture-pass")
        driver.mode("OBSERVE")
        second_pid = state(root, "fixture-pass")["pid"]
        driver.children.add(second_pid)
        driver.send(b"\r")
        driver.mode("INTERACT")
        driver.send(b"q")
        driver.wait(lambda: raw(root, "fixture-pass") == b"q", "unguarded q delivery")
        expected_pass = b"q"
        for native_key in (b"\x1b", b"\x03", b"\x1b[21~", b"Q"):
            driver.send(native_key)
            expected_pass += native_key
            driver.wait(lambda: raw(root, "fixture-pass") == expected_pass, "unguarded Esc/Ctrl+C/F10/Q delivery")
            driver.mode("INTERACT")
        driver.wait(lambda: "Esc: lazyset" in "".join(driver.screen.grid[-1]), "universal return hint without return_keys")
        driver.send(PREFIX + b"\x1b")
        driver.mode("OBSERVE")
        assert raw(root, "fixture-pass") == expected_pass, "prefix Esc reached the child"
        assert state(root, "fixture-pass")["pid"] == second_pid and alive(second_pid), "returning restarted the child"
        driver.send(b"\r")
        driver.mode("INTERACT")
        driver.send(PREFIX + b"p")
        driver.marker("fixture")
        driver.mode("OBSERVE")
        for index in range(100):
            driver.send(PREFIX + b"n")
            tag = "fixture-pass" if index % 2 == 0 else "fixture"
            driver.marker(tag)
            driver.mode("OBSERVE")
            assert state(root, "fixture")["pid"] == first_pid and alive(first_pid)
            assert state(root, "fixture-pass")["pid"] == second_pid and alive(second_pid)
            assert raw(root, "fixture") == expected and raw(root, "fixture-pass") == expected_pass, "switching changed child input state"
        print("PASS unguarded q/Esc/Ctrl+C/F10/Q, visible prefix-Esc return, and 100 switches preserve both PIDs", flush=True)

        driver.click_header("Interact")
        driver.mode("INTERACT")
        driver.click_header("Back")
        driver.mode("OBSERVE")
        driver.quiet()
        assert raw(root, "fixture") == expected, "host header mouse events leaked to child"
        for columns, rows in ((80, 24), (20, 6), (120, 36)):
            driver.resize(columns, rows)
            child_columns = max(1, columns - 32 if columns >= 100 else columns - 2)
            child_rows = max(1, rows - 8)
            driver.wait(lambda: all(state(root, tag).get("columns") == child_columns and
                                    state(root, tag).get("rows") == child_rows
                                    for tag in ("fixture", "fixture-pass")), f"resize to {columns}x{rows}")
            if columns < 24:
                driver.wait(lambda: "Terminal too small" in driver.screen.text(), "small terminal fallback")
            else:
                driver.marker("fixture")
        print("PASS mouse header ownership and resize of active/background PTYs", flush=True)

        # The first click into a live right pane focuses it and forwards the
        # complete gesture, including the release in child-relative cells.
        driver.click(33, 6)  # New content origin is (31, 4).
        driver.mode("INTERACT")
        expected += b"\x1b[<0;3;3M\x1b[<0;3;3m"
        driver.wait(lambda: raw(root, "fixture") == expected, "first focus click forwarded to child")
        driver.wait(lambda: "╔ INPUT" in driver.screen.text(), "visible focused terminal frame")
        driver.send(PREFIX + b"\x1b")
        driver.mode("OBSERVE")
        driver.wait(lambda: "╔ TOOLS" in driver.screen.text(), "visible focused tool list frame")
        print("PASS Observe click forwards first press/release and moves the highlighted frame", flush=True)

        driver.send(b"\r")
        driver.mode("INTERACT")
        driver.click_quit()
        driver.wait(lambda: "Confirm: quit" in driver.screen.text(), "mouse Quit from Interact")
        driver.click_text("[ Cancel ]")
        driver.marker("fixture")
        driver.mode("OBSERVE")
        assert alive(first_pid) and alive(second_pid), "cancelled mouse Quit stopped a child"
        assert raw(root, "fixture") == expected, "Quit click or popup input reached the child"
        print("PASS top-right Quit works during Interact; mouse Cancel keeps both sessions", flush=True)

        # Both discoverable entry points open the same searchable Commands menu.
        driver.send(b" ")
        driver.wait(lambda: "lazyset · Commands" in driver.screen.text(), "Space Commands menu")
        driver.send(b"\x1b")
        driver.marker("fixture")
        driver.send(b":q")
        driver.wait(lambda: "Quit lazyset" in driver.screen.text(), ":q matches Quit")
        assert alive(first_pid) and alive(second_pid), "typing q in the command menu executed Quit"
        driver.send(b"\r")
        driver.wait(lambda: "Confirm: quit" in driver.screen.text(), ":q confirmation")
        driver.send(b"\x1b")
        driver.marker("fixture")
        assert raw(root, "fixture") == expected, "command menu input leaked to the child"

        # Closing a page is distinct from exiting the workspace or selecting a
        # different tool. A cancelled close keeps its PID, and a confirmed close
        # removes it while a background session continues untouched.
        driver.send(b"x")
        driver.wait(lambda: "Confirm: close" in driver.screen.text(), "close current confirmation")
        assert "FIXTURE:fixture PID=" in driver.screen.text(), "close popup erased background context"
        driver.click(2, 1)  # Outside the popup; must not invoke the Hosts button.
        driver.quiet()
        assert "Confirm: close" in driver.screen.text(), "outside click escaped the close popup"
        driver.click_text("[ Cancel ]")
        driver.marker("fixture")
        assert alive(first_pid), "default close confirmation stopped a child"
        driver.send(PREFIX + b"x")
        driver.wait(lambda: "Confirm: close" in driver.screen.text(), "prefix close confirmation")
        driver.click_text("[ Confirm ]")
        driver.wait(lambda: not alive(first_pid), "closed current child termination")
        driver.wait(lambda: "[Sessions 1]" in driver.screen.text(), "closed page removed from sessions")
        assert alive(second_pid), "closing the current page stopped its background sibling"
        driver.send(b"\r")
        driver.wait(lambda: state(root, "fixture").get("pid", first_pid) != first_pid, "new launch after closing a page")
        replacement_pid = state(root, "fixture")["pid"]
        driver.children.add(replacement_pid)
        driver.marker("fixture")
        driver.mode("OBSERVE")

        # Closing a selected row in Sessions must target that row, not the
        # currently displayed page, and leave the updated list available.
        driver.send(b":sessions\r")
        driver.wait(lambda: "lazyset · Sessions" in driver.screen.text(), "Sessions command")
        driver.send(b"\x1b[Bx")
        driver.wait(lambda: "Confirm: close" in driver.screen.text() and "fixture-pass" in driver.screen.text(), "selected background close")
        driver.send(b"\x1b[C\r")
        driver.wait(lambda: not alive(second_pid), "selected background child termination")
        driver.wait(lambda: "lazyset · Sessions" in driver.screen.text() and "fixture-pass" not in driver.screen.text(), "updated Sessions list")
        assert alive(replacement_pid), "Sessions close stopped the active child instead of its selected row"
        driver.send(b"\x1b")
        driver.marker("fixture")
        assert raw(root, "fixture") == expected and raw(root, "fixture-pass") == expected_pass, "workspace command/close keys reached a child"
        print("PASS Space/: Commands, :q cancellation, x close/reopen and selected Sessions close", flush=True)

        # Saving an alias is configuration editing, and must never begin an SSH
        # connection or switch away from the current local child implicitly.
        original_config = config.read_bytes()
        hosts_config = config.with_name("hosts.toml")
        original_hosts = hosts_config.read_bytes() if hosts_config.exists() else None
        driver.send(b"Hn")
        driver.wait(lambda: "lazyset · Add SSH host" in driver.screen.text(), "Hosts add form")
        driver.send(b"discarded-qx.invalid\x1b")
        driver.wait(lambda: "Choose host" in " ".join(driver.screen.text().split()), "cancelled host form")
        assert config.read_bytes() == original_config, "cancelled host form modified main configuration"
        assert (hosts_config.read_bytes() if hosts_config.exists() else None) == original_hosts, "cancelled host form modified hosts configuration"
        driver.send(b"n")
        driver.wait(lambda: "lazyset · Add SSH host" in driver.screen.text(), "Hosts add form after cancel")
        driver.send(b"lazyset-pty-qx.invalid\tPTY saved host\x13")
        driver.wait(lambda: "Host saved." in driver.screen.text() and "PTY saved host" in driver.screen.text(), "saved SSH alias in Hosts")
        assert "lazyset-pty-qx.invalid" in hosts_config.read_text(), "host form did not save to hosts.toml"
        assert config.read_bytes() == original_config, "host form changed portable config.toml"
        driver.send(b"\x1b")
        driver.marker("fixture")
        driver.quiet()
        assert not ssh_called.exists(), "saving an SSH alias started a connection"
        assert alive(replacement_pid) and raw(root, "fixture") == expected, "host form changed active child or leaked typing"
        print("PASS Hosts n writes only hosts.toml; cancel/save causes no SSH or child input", flush=True)
        driver.quit(mouse=True)
        print("PASS mouse-confirmed Quit restores terminal modes and reaps owned children", flush=True)
    finally:
        driver.close()


def focus_only_smoke(binary: Path, root: Path):
    root.mkdir()
    config = config_file(root, ["fixture"], focus_click="focus-only")
    driver = Driver(binary, root, config, "fixture", environment(root))
    try:
        driver.marker("fixture")
        driver.children.add(state(root, "fixture")["pid"])
        driver.mode("OBSERVE")
        driver.send(b"\x1b[<0;34;7M")
        driver.mode("INTERACT")
        driver.send(b"\x1b[<32;1;1M\x1b[<0;1;1m")
        driver.quiet()
        assert raw(root, "fixture") == b"", "focus-only initial press/drag/release leaked to child"
        driver.click(33, 6)
        expected = b"\x1b[<0;3;3M\x1b[<0;3;3m"
        driver.wait(lambda: raw(root, "fixture") == expected, "second click after focus-only")
        driver.quit()
        print("PASS focus-only consumes the entire initial gesture; the next click reaches the child", flush=True)
    finally:
        driver.close()


def visibility_smoke(binary: Path, root: Path):
    root.mkdir()
    config = config_file(root, ["fixture", "fixture-pass", "fixture-missing"])
    driver = Driver(binary, root, config, "fixture", environment(root))
    try:
        driver.marker("fixture")
        child_pid = state(root, "fixture")["pid"]
        driver.children.add(child_pid)

        def visible(tag: str) -> bool:
            return any(line.rstrip().endswith(" " + tag) for line in driver.sidebar_text().splitlines())

        def only(index: int, label: str):
            driver.send(b"f")
            driver.wait(lambda: "Visible tool statuses" in driver.screen.text(), "status filter menu")
            driver.send(b"j" * index + b"o\r")
            driver.wait(lambda: "Showing: " + label in driver.screen.text(), label + " filter")
            driver.marker("fixture")
            assert state(root, "fixture")["pid"] == child_pid and alive(child_pid), "visibility filter replaced/stopped active session"
            assert raw(root, "fixture") == b"", "visibility keys reached the active child"

        only(0, "Running")
        assert visible("fixture") and not visible("fixture-pass") and not visible("fixture-missing"), "Running filter exposed non-running rows"
        only(1, "Available")
        assert visible("fixture-pass") and not visible("fixture") and not visible("fixture-missing"), "Available filter did not hide live/missing rows"
        only(2, "Not installed")
        assert visible("fixture-missing") and not visible("fixture") and not visible("fixture-pass"), "Not installed filter mixed availability states"
        assert not (root / "fixture-pass.json").exists(), "browsing visibility eagerly launched an available tool"
        driver.send(b"f")
        driver.wait(lambda: "Visible tool statuses" in driver.screen.text(), "restore all status filters")
        driver.send(b"a\r")
        driver.wait(lambda: "All states" in driver.screen.text(), "all statuses restored")
        driver.quit()
        print("PASS Running/Available/Not installed filters hide rows without switching the active screen", flush=True)
    finally:
        driver.close()


def startup_smoke(binary: Path, root: Path):
    root.mkdir()
    config = config_file(root, ["fixture", "fixture-pass"])
    external_state = root / "fixture-external.json"
    command = [sys.executable, "-u", str(root / "fixture.py"), "fixture-external",
               str(root / "fixture-external.raw"), str(external_state)]
    with config.open("a") as output:
        output.write('\n[[tools]]\nid = "fixture-external"\nmode = "external"\ncommand = ' + json.dumps(command) + '\n')
        output.write('\n[[sets]]\nid = "startup-batch"\nname = "Startup batch"\ntools = ' +
                     json.dumps(["fixture", "fixture-pass", "fixture-missing", "fixture-external"]) + '\n')
    args = ["--start-set", "startup-batch", "--start-set", "unknown-startup-set",
            "--", "fixture", "fixture-pass", "unknown-startup-tool", "fixture"]
    driver = Driver(binary, root, config, None, environment(root), args=args)
    try:
        driver.wait(lambda: bool(state(root, "fixture")) and bool(state(root, "fixture-pass")), "both positional startup sessions")
        first_pid, second_pid = state(root, "fixture")["pid"], state(root, "fixture-pass")["pid"]
        driver.children.update((first_pid, second_pid))
        driver.marker("fixture")
        driver.mode("OBSERVE")
        driver.wait(lambda: "[Sessions 2]" in driver.screen.text(), "deduplicated startup sessions")
        assert not external_state.exists(), "batch startup took over the terminal with an external tool"
        driver.send(b":startup\r")
        driver.wait(lambda: "Startup results" in driver.screen.text(), "startup report")
        driver.wait(lambda: "0 queued" in driver.screen.text() and "0 starting" in driver.screen.text(), "startup completed report")
        report = driver.screen.text()
        for expected in ("unknown-startup-tool", "unknown-startup-set", "fixture-missing: missing",
                         "fixture-external: external", "Started: local/fixture", "Started: local/fixture-pass"):
            assert expected in report, f"startup report omitted {expected!r}:\n{report}"
        assert state(root, "fixture")["pid"] == first_pid and state(root, "fixture-pass")["pid"] == second_pid, "duplicate startup request restarted a session"
        assert raw(root, "fixture") == b"" and raw(root, "fixture-pass") == b"", "startup report input reached a child"
        driver.send(b"\x1b")
        driver.marker("fixture")
        driver.quit()
        print("PASS positional/--start-set batch startup deduplicates sessions and reports unknown/missing/external skips", flush=True)
    finally:
        driver.close()


def handoff_smoke(binary: Path, root: Path):
    """Verify Bubble Tea releases/reacquires the real TTY around native tools."""
    root.mkdir()
    config = config_file(root, ["fixture", "external-native"])
    editor = root / "native_editor.py"
    editor.write_text(NATIVE_EDITOR)
    external = root / "native_external.py"
    external.write_text(NATIVE_EXTERNAL)
    with config.open("a") as output:
        output.write('\n[[tools]]\nid = "external-native"\nname = "external-native"\n')
        output.write('mode = "external"\nq_to_observe = true\ncommand = ' +
                     json.dumps([sys.executable, "-u", str(external), str(root)]) + '\n')
    env = environment(root)
    env["VISUAL"] = ""
    env["EDITOR"] = json.dumps(sys.executable) + " -u " + json.dumps(str(editor))
    env["LAZYSET_PTY_HANDOFF_ROOT"] = str(root)
    driver = Driver(binary, root, config, "fixture", env)
    try:
        driver.marker("fixture")
        driver.mode("OBSERVE")
        fixture_pid = state(root, "fixture")["pid"]
        driver.children.add(fixture_pid)

        def preserve_fixture():
            assert state(root, "fixture")["pid"] == fixture_pid and alive(fixture_pid), "native handoff replaced/stopped live fixture"
            assert raw(root, "fixture") == b"", "native handoff input leaked into embedded fixture"

        def edit(mode: str):
            (root / "editor-mode.txt").write_text(mode)
            driver.send(driver.prefix + b"/")
            driver.wait(lambda: "lazyset · Commands" in driver.screen.text(), "Commands menu")
            label = "Edit hosts configuration" if mode == "hosts" else "Edit configuration"
            driver.send(label.encode())
            driver.wait(lambda: "> " + label in driver.screen.text(), "filtered editor action")
            offset = len(driver.transcript)
            driver.send(b"\r")
            driver.wait(lambda: f"NATIVE_EDITOR_READY:{mode}".encode() in driver.transcript[offset:], f"native editor {mode}")
            driver.wait(lambda: state(root, "editor").get("mode") == mode, "editor arguments/TTY record")
            record = state(root, "editor")
            driver.children.add(record["pid"])
            expected_path = config.with_name("hosts.toml") if mode == "hosts" else config
            assert Path(record["path"]) == expected_path, "editor did not receive actual config path"
            assert record["stdin_tty"] and record["stdout_tty"] and record["canonical"] and record["echo"], "editor did not receive the restored native TTY"
            driver.send(b"save\r")
            driver.wait(lambda: state(root, "editor").get("saved") and state(root, "editor").get("mode") == mode, "editor save")
            assert state(root, "editor")["input"] == "save\n", "native editor input was intercepted"
            wanted = "Configuration validation:" if mode == "invalid" else "Configuration reloaded."
            driver.wait(lambda: wanted in driver.screen.text(), f"editor result {mode}")
            driver.mode("OBSERVE")
            preserve_fixture()

        edit("valid")
        assert '# pty editor valid' in config.read_text()
        driver.prefix = b"\x07"  # The edited effective config now uses Ctrl+G.
        driver.send(driver.prefix + b"?")
        driver.wait(lambda: "Keyboard help" in driver.screen.text() and
                           "prefix: ctrl+g" in driver.screen.text().lower(), "reloaded effective prefix")
        driver.send(b"\x1b")
        driver.mode("OBSERVE")
        print("PASS native editor gets config path/restored TTY; valid edit reloads prefix and preserves session", flush=True)

        edit("invalid")
        assert config.read_text() == "prefix = [unterminated\n", "malformed editor file was overwritten"
        edit("repair")
        assert '# pty editor repaired' in config.read_text()
        driver.marker("fixture")
        print("PASS malformed edit stays visible; a second native edit repairs config without losing session", flush=True)

        portable_config = config.read_bytes()
        edit("hosts")
        assert config.read_bytes() == portable_config, "hosts editor changed portable config.toml"
        hosts_path = config.with_name("hosts.toml")
        assert "# pty hosts editor" in hosts_path.read_text(), "hosts editor did not write the separate file"
        reported_path = subprocess.check_output(
            [str(binary), "--config", str(config), "config", "path", "--hosts"],
            cwd=root, env=env, text=True,
        ).strip()
        assert Path(reported_path) == hosts_path, "CLI hosts path disagrees with native hosts editor"
        print("PASS native hosts editor and config path --hosts target hosts.toml without modifying config.toml", flush=True)

        offset = len(driver.transcript)
        driver.send(b"j\r")
        driver.wait(lambda: b"NATIVE_EXTERNAL_READY" in driver.transcript[offset:], "native external tool")
        record = state(root, "external")
        driver.children.add(record["pid"])
        assert record["stdin_tty"] and record["stdout_tty"] and record["canonical"] and record["echo"], "external tool did not receive restored native TTY"
        assert (record["columns"], record["rows"]) == (120, 36), "external tool did not receive the full outer terminal"
        driver.send(b"q")
        driver.wait(lambda: raw(root, "external") == b"q", "native external q delivery")
        driver.wait(lambda: "External tool finished." in driver.screen.text(), "return from external tool")
        driver.mode("OBSERVE")
        preserve_fixture()
        assert not alive(record["pid"]), "external process was not reaped"
        driver.quit()
        print("PASS external handoff owns q/full terminal and returns with live embedded session intact", flush=True)
    finally:
        driver.close()


def process_rows() -> list[tuple[int, int, str]]:
    output = subprocess.check_output(["ps", "-axo", "pid=,ppid=,comm="], text=True)
    result = []
    for line in output.splitlines():
        fields = line.strip().split(None, 2)
        if len(fields) == 3:
            result.append((int(fields[0]), int(fields[1]), fields[2]))
    return result


def monitor_smoke(binary: Path, root: Path, monitor: str):
    root.mkdir()
    driver = Driver(binary, root, config_file(root, [monitor, "fixture"]), monitor, environment(root))
    try:
        child_pid = None

        def running_monitor():
            nonlocal child_pid
            for pid, parent, command in process_rows():
                if parent == driver.process.pid and Path(command).name == monitor:
                    child_pid = pid
                    return True
            return False

        driver.wait(running_monitor, f"real {monitor} process")
        driver.children.add(child_pid)
        driver.wait(lambda: f"Local / {monitor}" in driver.screen.text(), f"{monitor} caption")
        driver.wait(lambda: any(label in "\n".join("".join(row[31:-1]) for row in driver.screen.grid[4:-4]).lower()
                               for label in ("cpu", "mem", "tasks", "pid")), f"{monitor} output in pane")
        driver.send(b"\r")
        driver.mode("INTERACT")
        driver.send(b"q")
        driver.mode("OBSERVE")
        assert alive(child_pid), f"q closed real {monitor}"
        driver.send(PREFIX + b"n")
        driver.marker("fixture")
        driver.children.add(state(root, "fixture")["pid"])
        driver.send(PREFIX + b"p")
        driver.wait(lambda: f"Local / {monitor}" in driver.screen.text(), f"return to {monitor}")
        assert alive(child_pid), f"switching restarted or stopped {monitor}"
        assert any(pid == child_pid and parent == driver.process.pid for pid, parent, _ in process_rows())
        driver.resize(90, 28)
        driver.mode("OBSERVE")
        assert alive(child_pid)
        driver.quit()
        print(f"PASS installed {monitor}: rendered metrics, q guard, switch/PID retention, resize and cleanup", flush=True)
    finally:
        driver.close()


def lazychezmoi_smoke(binary: Path, root: Path):
    """Exercise the reported tool with every native chezmoi path disposable."""
    tool_binary, chezmoi_binary = shutil.which("lazychezmoi"), shutil.which("chezmoi")
    if not tool_binary or not chezmoi_binary:
        print("SKIP lazychezmoi: lazychezmoi and chezmoi must both be installed", flush=True)
        return
    root.mkdir()
    source, destination, cache = root / "source", root / "destination", root / "chezmoi-cache"
    for path in (source, destination, cache):
        path.mkdir()
    native_config, preferences = root / "chezmoi.toml", root / "lazychezmoi.toml"
    native_config.write_text("")
    preferences.write_text("")
    (source / "dot_guard").write_text("isolated source fixture\n")
    (destination / ".guard").write_text("isolated destination fixture\n")
    command = [tool_binary, "--config", str(preferences), "--chezmoi", chezmoi_binary,
               "--source", str(source), "--destination", str(destination),
               "--working-tree", str(source), "--chezmoi-config", str(native_config),
               "--chezmoi-cache", str(cache), "--persistent-state", str(root / "chezmoi-state.db"),
               "--auto-fetch=false", "--diff-renderer=builtin", "tui"]
    config = config_file(root, ["lazychezmoi", "fixture"])
    with config.open("a") as output:
        output.write('\n[[tools]]\nid = "lazychezmoi"\ncommand = ' + json.dumps(command) + '\n')
    driver = Driver(binary, root, config, "lazychezmoi", environment(root))
    try:
        child_pid = None

        def running_tool():
            nonlocal child_pid
            for pid, parent, executable in process_rows():
                if parent == driver.process.pid and Path(executable).name == "lazychezmoi":
                    child_pid = pid
                    return True
            return False

        driver.wait(running_tool, "isolated real lazychezmoi process")
        driver.children.add(child_pid)
        driver.wait(lambda: ".guard" in "\n".join("".join(row[31:-1]) for row in driver.screen.grid[4:-4]),
                    "isolated managed file inside lazychezmoi", timeout=15)
        driver.mode("OBSERVE")
        driver.send(b"\r")
        driver.mode("INTERACT")
        driver.send(b"q")
        driver.mode("OBSERVE")
        assert alive(child_pid), "guarded q exited real lazychezmoi"
        driver.send(b"\r")
        driver.mode("INTERACT")
        driver.send(b":")
        driver.wait(lambda: "Type to search actions" in driver.screen.text(), "lazychezmoi Actions dialog")
        driver.send(b"\x03")
        driver.wait(lambda: "Type to search actions" not in driver.screen.text() and ".guard" in driver.screen.text(),
                    "Ctrl+C closes native lazychezmoi dialog")
        driver.mode("INTERACT")
        assert alive(child_pid), "Ctrl+C in a native dialog exited lazychezmoi"
        driver.send(PREFIX + b"\x1b")
        driver.mode("OBSERVE")
        assert any(pid == child_pid and parent == driver.process.pid for pid, parent, _ in process_rows()), "lazychezmoi PID was replaced"
        driver.send(PREFIX + b"n")
        driver.marker("fixture")
        driver.children.add(state(root, "fixture")["pid"])
        driver.send(PREFIX + b"p")
        driver.wait(lambda: "Local / lazychezmoi" in driver.screen.text(), "retained lazychezmoi after switching")
        assert alive(child_pid), "switching terminated real lazychezmoi"
        assert (source / "dot_guard").read_text() == "isolated source fixture\n", "guard test edited source fixture"
        assert (destination / ".guard").read_text() == "isolated destination fixture\n", "guard test applied destination fixture"
        driver.quit()
        print("PASS installed lazychezmoi: isolated paths, q guard, native Ctrl+C dialog cancel, prefix-Esc/PID retention and cleanup", flush=True)
    finally:
        driver.close()


def dev_smoke(binary: Path, root: Path):
    """Close real dev help without invoking repo navigation or runtime handoff."""
    tool_binary = shutil.which("dev")
    if not tool_binary:
        print("SKIP dev: not installed on PATH", flush=True)
        return
    root.mkdir()
    paths = {name: root / name for name in ("repos", "tries", "worktrees", "dev-state")}
    for path in paths.values():
        path.mkdir()
    native_config = root / "dev.toml"
    native_config.write_text("\n".join([
        "[paths]", "scan_roots = " + json.dumps([str(paths["repos"])]), "repo_paths = []",
        "project_root = " + json.dumps(str(paths["repos"])),
        "tries_root = " + json.dumps(str(paths["tries"])),
        "worktree_root = " + json.dumps(str(paths["worktrees"])),
        "state_dir = " + json.dumps(str(paths["dev-state"])),
        '[runtime]', 'backend = "none"',
        '[stats]', 'sampler = false', 'wakatime = false',
        '[update]', 'check = false',
        '[tui.fleet]', 'background_refresh = false',
        '[tui.ssh]', 'background_refresh = false', "",
    ]))
    remotes = root / "dev-remotes.toml"
    remotes.write_text("")
    command = [tool_binary, "--config", str(native_config), "--remotes", str(remotes),
               "--no-runtime", "tui"]
    config = config_file(root, ["dev", "fixture"])
    with config.open("a") as output:
        output.write('\n[[tools]]\nid = "dev"\ncommand = ' + json.dumps(command) + '\n')
    env = environment(root)
    env["DEV_NO_UPDATE_CHECK"] = "1"
    driver = Driver(binary, root, config, "dev", env)
    try:
        child_pid = None

        def running_tool():
            nonlocal child_pid
            for pid, parent, executable in process_rows():
                if parent == driver.process.pid and Path(executable).name == "dev":
                    child_pid = pid
                    return True
            return False

        driver.wait(running_tool, "isolated real dev process")
        driver.children.add(child_pid)
        driver.wait(lambda: "TASKS" in driver.screen.text() and "REPOS" in driver.screen.text(),
                    "dev dashboard rendered", timeout=15)
        driver.mode("OBSERVE")
        driver.send(b"\r")
        driver.mode("INTERACT")
        driver.send(b"?")
        driver.wait(lambda: "Help · TASKS" in driver.screen.text(), "dev native Help popup")
        driver.send(b"\x1b")
        driver.wait(lambda: "Help · TASKS" not in driver.screen.text() and "TASKS" in driver.screen.text(),
                    "Esc closes dev native Help popup")
        driver.mode("INTERACT")
        assert alive(child_pid), "native Esc exited dev instead of closing Help"
        driver.wait(lambda: "Esc: lazyset" in "".join(driver.screen.grid[-1]), "visible return hint with q guard")
        driver.send(PREFIX + b"\x1b")
        driver.mode("OBSERVE")
        assert any(pid == child_pid and parent == driver.process.pid for pid, parent, _ in process_rows()), "prefix Esc replaced dev PID"
        driver.send(b"\r")
        driver.mode("INTERACT")
        driver.send(b"q")
        driver.mode("OBSERVE")
        assert alive(child_pid), "q guard exited real dev"
        driver.quit(mouse=True)
        print("PASS installed dev: disposable paths/no runtime, Esc closes native Help, q guard, prefix-Esc/PID retention and mouse Quit", flush=True)
    finally:
        driver.close()


def superfile_smoke(binary: Path, root: Path):
    """Exercise installed spf against disposable native paths and home files."""
    tool_binary = shutil.which("spf")
    if not tool_binary:
        print("SKIP Superfile: spf is not installed on PATH", flush=True)
        return
    root.mkdir()
    env = environment(root)
    native_home = root / "home"
    native_home.mkdir()
    # Superfile uses adrg/xdg for config/data/state/cache and HOME for browsing
    # and Darwin's .Trash. Override only this subprocess environment map.
    env["HOME"] = str(native_home)
    fixture = native_home / "superfile-home-fixture.txt"
    fixture.write_text("isolated Superfile home fixture\n")
    native_config = root / "superfile.toml"
    native_config.write_text("\n".join([
        "auto_check_update = false", "ignore_missing_fields = true",
        "default_open_file_preview = false", "show_image_preview = false", "",
    ]))
    native_data = Path(env["XDG_DATA_HOME"]) / "superfile"
    native_data.mkdir()
    # Skip first-run onboarding; hotkeys/themes are generated by the installed
    # binary inside the disposable XDG tree, without copying personal config.
    (native_data / "firstUseCheck").touch()
    config = config_file(root, ["superfile", "fixture"])
    with config.open("a") as output:
        output.write('\n[[tools]]\nid = "superfile"\ncommand = ' +
                     json.dumps([tool_binary, "--config-file", str(native_config)]) + '\n')
    driver = Driver(binary, root, config, "superfile", env)
    try:
        child_pid = None

        def running_tool():
            nonlocal child_pid
            for pid, parent, executable in process_rows():
                if parent == driver.process.pid and Path(executable).name == "spf":
                    child_pid = pid
                    return True
            return False

        def pane_text():
            return "\n".join("".join(row[31:-1]) for row in driver.screen.grid[4:-4])

        driver.wait(running_tool, "isolated real Superfile process")
        driver.children.add(child_pid)
        driver.wait(lambda: fixture.name in pane_text(), "Superfile starts in disposable home", timeout=15)
        driver.mode("OBSERVE")
        driver.send(b"\r")
        driver.mode("INTERACT")
        driver.send(b"q")
        driver.mode("OBSERVE")
        assert alive(child_pid), "guarded q exited Superfile"
        driver.send(b"\r")
        driver.mode("INTERACT")
        driver.send(b"?")
        driver.wait(lambda: "Open help menu (hotkeylist)" in pane_text(), "Superfile native Help popup")
        driver.send(b"\x1b")
        driver.wait(lambda: "Open help menu (hotkeylist)" not in pane_text() and fixture.name in pane_text(),
                    "Esc closes Superfile native Help popup")
        driver.mode("INTERACT")
        assert alive(child_pid), "native Esc exited Superfile instead of closing Help"
        driver.send(PREFIX + b"\x1b")
        driver.mode("OBSERVE")
        assert any(pid == child_pid and parent == driver.process.pid for pid, parent, _ in process_rows())
        driver.send(PREFIX + b"n")
        driver.marker("fixture")
        driver.children.add(state(root, "fixture")["pid"])
        driver.send(PREFIX + b"p")
        driver.wait(lambda: "Local / Superfile" in driver.screen.text(), "retained Superfile after switching")
        assert alive(child_pid), "switching terminated Superfile"
        driver.send(b"\r")
        driver.mode("INTERACT")
        driver.send(PREFIX + b"q")
        driver.wait(lambda: "Superfile exited (status 0)" in pane_text(), "Superfile native exit card")
        driver.mode("OBSERVE")
        driver.wait(lambda: not alive(child_pid), "native Superfile process has exited")
        driver.quiet(0.25)
        assert not any(parent == driver.process.pid and Path(executable).name == "spf"
                       for _, parent, executable in process_rows()), "Superfile auto-restarted"
        previous_pid = child_pid
        driver.send(b":reopen\r")
        driver.wait(running_tool, "explicitly reopened Superfile process")
        driver.children.add(child_pid)
        assert child_pid != previous_pid, "Reopen reused the exited Superfile process"
        driver.wait(lambda: fixture.name in pane_text(), "reopened Superfile home")
        assert fixture.read_text() == "isolated Superfile home fixture\n", "Superfile modified its fixture"
        assert not (native_data / "lastCheckVersion").exists(), "Superfile unexpectedly checked for updates"
        driver.quit(mouse=True)
        print("PASS installed Superfile: disposable HOME/XDG, home default, q guard, native Esc popup cancel, "
              "switch/PID retention, native exit/Reopen and cleanup", flush=True)
    finally:
        driver.close()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("binary", nargs="?", default="bin/lazyset", help="built lazyset executable (default: bin/lazyset)")
    parser.add_argument("--real-monitors", action="store_true", help="also smoke-test any installed btop/htop")
    parser.add_argument("--real-lazychezmoi", action="store_true", help="also test installed lazychezmoi with disposable native paths")
    parser.add_argument("--real-dev", action="store_true", help="also test installed dev help with disposable paths and no runtime")
    parser.add_argument("--real-superfile", action="store_true", help="also test installed spf with disposable HOME/XDG paths and updates disabled")
    parser.add_argument("--handoffs-only", action="store_true", help="run only editor/external handoff checks")
    parser.add_argument("--keep", action="store_true", help="retain temporary transcripts and fixtures after success")
    args = parser.parse_args()
    binary = Path(args.binary).resolve()
    if not binary.is_file() or not os.access(binary, os.X_OK):
        parser.error(f"build the executable first: go build -o {binary} ./cmd/lazyset")
    root = Path(tempfile.mkdtemp(prefix="lazyset-pty-"))
    success = False
    try:
        if not args.handoffs_only:
            fixture_smoke(binary, root / "fixture")
            focus_only_smoke(binary, root / "focus-only")
            visibility_smoke(binary, root / "visibility")
            startup_smoke(binary, root / "startup")
        handoff_smoke(binary, root / "handoffs")
        if args.real_monitors and not args.handoffs_only:
            for monitor in ("btop", "htop"):
                if shutil.which(monitor):
                    monitor_smoke(binary, root / monitor, monitor)
                else:
                    print(f"SKIP {monitor}: not installed on PATH", flush=True)
        if args.real_lazychezmoi and not args.handoffs_only:
            lazychezmoi_smoke(binary, root / "lazychezmoi")
        if args.real_dev and not args.handoffs_only:
            dev_smoke(binary, root / "dev")
        if args.real_superfile and not args.handoffs_only:
            superfile_smoke(binary, root / "superfile")
        success = True
        print("PTY smoke passed. This verifies input/lifecycle behavior, not Unicode visual fidelity.")
        return 0
    except (AssertionError, OSError, termios.error, subprocess.SubprocessError, KeyboardInterrupt) as error:
        print(f"FAIL: {error}", file=sys.stderr)
        return 1
    finally:
        if success and not args.keep:
            shutil.rmtree(root)
        else:
            print(f"PTY artifacts retained: {root}", file=sys.stderr)


if __name__ == "__main__":
    raise SystemExit(main())
