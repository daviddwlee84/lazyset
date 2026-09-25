# Project agent guidance

## MANDATORY: Use td for Task Management

You must run td usage --new-session at conversation start (or after /clear) to see current work.
Use td usage -q for subsequent reads.

lazyset is an experimental Go TUI catalog and workspace for local and OpenSSH
hosts. It discovers known executables, explains their purpose, organizes tool
sets, and switches between retained child sessions. The working product name
is lazyset; it is not limited to monitoring and does not implement its own metrics.

## Development

- Go version and pinned dependencies are declared in `go.mod`.
- Run: `go run ./cmd/lazyset`.
- Build: `go build -o ./bin/lazyset ./cmd/lazyset`.
- Checks: `go test ./...`, `go test -race ./...`, `go vet ./...`.
- PTY checks after building: `python3 scripts/pty_smoke.py bin/lazyset --real-monitors --real-lazychezmoi --real-dev --real-superfile`.
- Example configuration: `go run ./cmd/lazyset --config ./examples/config.toml config validate`.
- macOS/Linux binaries and the personal tap are supported; see docs/distribution.md.
  `upgrade --check` is read-only; apply delegates verified Homebrew ownership.
  Plain source builds report `dev`; tagged Go installs recover their module version.

## Architecture and contracts

- Cobra CLI lives in `internal/cli`; the entrypoint is `cmd/lazyset`.
  Domain types, config/catalog, host discovery/SSH, terminal sessions, and the
  Bubble Tea v2 UI are separate packages under `internal`.
- Terminal embedding uses PTYs and `github.com/charmbracelet/x/vt`. The outer
  Bubble Tea program is the sole reader of the real terminal. Editor, SSH
  authentication, and external-tool handoffs must release that ownership.
- Default input mode is Observe. Enter activates a selected tool, then Enter
  again enables Interact. Clicking the live terminal also enters Interact;
  `focus_click = "forward"` sends that click through, while `focus-only` consumes
  it. Focus is shown by pane borders and text. Navigation does not eagerly spawn
  all tools. Only the configured prefix and per-tool `return_keys` intercept
  child keys; `q_to_observe` remains a legacy q-only override.
- Built-in return defaults guard only lowercase q where it is a native exit key;
  translate and k9s have none. Native quit metadata is independent of guards.
  Esc/Ctrl+C/F10/Q retain native behavior unless explicitly overridden. Interact
  keeps the configured prefix → Esc return hint visible before secondary hints.
- Plain `q` must never quit the workspace. A child exit remains an exit state;
  do not auto-select another child or auto-restart it. Enter on the current
  exited session or explicit Reopen starts a fresh process once. Prefix `q`
  sends literal q; prefix `v` passes the next key through return protection;
  prefix `Q` requests workspace exit. The default prefix is Ctrl+\.
- Observe Space / `:` and prefix `:` open Commands; command text owns printable
  keys. `:q` / `:quit` request workspace exit. Close (`x`, prefix `x`, or `:close`)
  removes a session, confirming termination if it is live; Stop retains its exit
  view. Confirmation uses a centered popup over the workspace. In Sessions, `x`
  targets that selected session rather than the active one.
- Top-right Quit shares the quit action and Tab focus order with workspace
  controls. Rendering and hit testing share button coordinates; modal input
  capture prevents background activation. Running/queued sessions confirm exit.
- Startup tool IDs are accepted only after `--`, with `--tool` as the one-tool
  alias. Repeatable `--start-set` expands set membership; `--set` only selects
  a view. Startup deduplicates requests, waits for fresh host discovery, and
  skips unknown/missing/external tools with retained `:startup` diagnostics.
  Unknown host/view/flag names remain usage errors. Never treat startup IDs as
  shell commands or arguments to a child.
- Discovery checks catalog/custom commands against each host's effective PATH;
  it cannot classify arbitrary executables as TUIs. Unknown observations must
  remain distinct from missing tools. Installation is guidance only.
  Cached availability is reference-only until fresh discovery succeeds; do not
  authorize a new launch from cached or failed observations.
- Built-in sets are immutable; users copy them to new IDs. Custom set writes
  preserve unrelated TOML/comments and use a snapshot to detect intervening edits.
- Built-in `all` and Explore are the same view, including effective custom tools.
  The Explore toggle remembers the previous set and preserves legacy selection
  history. Copies of All retain fixed membership. `personal` groups independently
  installed personal tools; catalog entries do not configure their backends.
- All and individual sets stay flat; `f` / `:visibility` selects visible Running,
  Available, Not installed, and Other statuses per host/view. Unknown and
  unsupported remain distinct from missing. Visibility is XDG state, not config.
- Config uses XDG explicitly on macOS and Linux. Config, durable selection
  state, and discovery cache belong in separate XDG directories. Help and path
  inspection must not create state or depend on parseable config.
- Portable preferences/tools/sets use config.toml; machine-local hosts and
  default_host use sibling hosts.toml. The sibling follows the logical selected
  config path, not its symlink target. `--hosts-config` overrides it; config
  path/edit `--hosts` targets that file. Legacy hosts in config remain readable
  with a warning, and same-ID hosts-file records replace them as whole records.
  Production reads/writes pass config.Sources through the source-aware APIs;
  preserve both flags when repairing malformed configuration.
- SSH uses OpenSSH configuration. Background checks are noninteractive;
  authentication is an explicit native-terminal handoff. Never shut down a
  borrowed/shared ControlMaster during cleanup.
- Add host (Hosts `n` / `:host-add`) accepts an SSH target and optional display name;
  Ctrl+S only saves it to hosts.toml, without probing or connecting. Add-only writes use the
  same snapshot/atomic/symlink safeguards as sets and preserve existing TOML.
- Yazi and Superfile (`spf`, tool ID `superfile`) default to `dir = "~"`;
  home-relative tool directories resolve against
  the selected host's effective HOME, including SSH hosts. Other tools retain
  their configured directory; local lazygit defaults to the invocation directory.
- Sessions persist only while this workspace runs. Saved selection/filter
  state is not process persistence. Keep hidden PTYs drained.
- dev-cli stays embedded and retains its own runtime handoff behavior. Its
  repo/task navigation can exit dev and activate an outer runtime; no restart
  loop, environment trick, or output parsing infers a completed handoff. Reopen
  starts dev anew. `:external` is an explicit native-terminal launch and refuses
  to duplicate a live embedded session for the same host/tool.
- Machine output must remain valid JSON with diagnostics on stderr. Config
  inspection redacts every environment value; editor invocation parses quoted
  arguments without shell evaluation. Human display fields and diagnostics
  strip terminal controls; JSON preserves original data with JSON escaping.

For terminal changes, use model/service tests and a real PTY; screenshot-only
checks do not establish input routing or terminal restoration. Keep tests on
disposable paths/backends rather than changing user tools, credentials, or config.
macOS PTY checks include real btop/htop, lazychezmoi with isolated native paths,
dev with disposable paths and runtime disabled, and Superfile with disposable
HOME/XDG paths and update checks/previews disabled. They verify native popup
cancellation, guarded q, prefix return, mouse Quit and terminal restoration.
Linux is cross-built only so far; no real SSH host has been verified. Do not
represent these as Linux-runtime or remote-host compatibility evidence.

## Binary distribution

See `docs/distribution.md`. Run GoReleaser config/snapshot checks and
`scripts/check-distribution.py` before tagging. Preserve immutable releases and
source/module exclusions. Backend setup is separate from installing this CLI.
