// Package cli exposes the same catalog and discovery services used by the TUI.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/daviddwlee84/lazyset/internal/managedupgrade"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"text/tabwriter"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/daviddwlee84/lazyset/internal/config"
	"github.com/daviddwlee84/lazyset/internal/core"
	"github.com/daviddwlee84/lazyset/internal/host"
	"github.com/daviddwlee84/lazyset/internal/tui"
	"github.com/spf13/cobra"
)

type DiscoveryManager interface {
	Discover(context.Context, core.Host, []core.Tool) ([]core.Availability, error)
	Close() error
}

// Dependencies keeps command intent, terminal gating, and machine output
// testable without opening a terminal, connecting to SSH, or launching editors.
type Dependencies struct {
	In         io.Reader
	Out        io.Writer
	ErrOut     io.Writer
	IsTerminal func(io.Reader, io.Writer) bool
	RunTUI     func(core.Config, string, tui.Options) error
	NewManager func() DiscoveryManager
	RunEditor  func(*exec.Cmd) error
}

func (d Dependencies) defaults() Dependencies {
	if d.In == nil {
		d.In = os.Stdin
	}
	if d.Out == nil {
		d.Out = os.Stdout
	}
	if d.ErrOut == nil {
		d.ErrOut = os.Stderr
	}
	if d.IsTerminal == nil {
		d.IsTerminal = isTerminalPair
	}
	if d.RunTUI == nil {
		d.RunTUI = tui.Run
	}
	if d.NewManager == nil {
		d.NewManager = func() DiscoveryManager { return host.NewManager() }
	}
	if d.RunEditor == nil {
		d.RunEditor = func(cmd *exec.Cmd) error { return cmd.Run() }
	}
	return d
}

func isTerminalPair(in io.Reader, out io.Writer) bool {
	input, ok := in.(interface{ Fd() uintptr })
	if !ok {
		return false
	}
	output, ok := out.(interface{ Fd() uintptr })
	if !ok {
		return false
	}
	return term.IsTerminal(input.Fd()) && term.IsTerminal(output.Fd())
}

type commandError struct {
	code int
	err  error
}

func (e *commandError) Error() string { return e.err.Error() }
func (e *commandError) Unwrap() error { return e.err }
func usageError(err error) error {
	if err == nil {
		return nil
	}
	return &commandError{2, err}
}

// ExitCode separates invalid invocation/configuration from operational failure.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, context.Canceled) {
		return 130
	}
	var failure *commandError
	if errors.As(err, &failure) {
		return failure.code
	}
	if strings.HasPrefix(err.Error(), "unknown command ") {
		return 2
	}
	return 1
}

func noArgs(cmd *cobra.Command, args []string) error { return usageError(cobra.NoArgs(cmd, args)) }

// Tool IDs are deliberately accepted only after --. Ordinary typos still read
// as unknown commands, rather than silently becoming startup requests.
func dashboardArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 || cmd.ArgsLenAtDash() == 0 {
		return nil
	}
	return usageError(fmt.Errorf("unknown command %q for lazyset; use lazyset -- TOOL_ID to start tools", args[0]))
}

// NewRoot does not load configuration or perform discovery until a command
// actually needs them. Help, version, and config path also work with bad TOML.
func NewRoot(version string, deps Dependencies) *cobra.Command {
	deps = deps.defaults()
	if version == "" {
		version = "dev"
	}
	version = humanText(version)
	var configPath, hostsPath string
	var opts tui.Options
	root := &cobra.Command{
		Use: "lazyset [flags] [-- TOOL_ID...]", Short: "Discover terminal tools and switch between local and SSH sessions",
		Long:    "lazyset is a TUI catalog and workspace. Bare invocation opens the dashboard in a terminal.\nWithout a terminal, use tools list --json, sets list --json, or config commands.",
		Version: version, Args: dashboardArgs, SilenceErrors: true, SilenceUsage: true,
		Example: "  lazyset\n  lazyset --host server --set gpu\n  lazyset -- btop nvtop\n  lazyset --start-set system --start-set gpu\n  lazyset tools list --installed --json\n  lazyset config edit --hosts",
	}
	root.SetIn(deps.In)
	root.SetOut(deps.Out)
	root.SetErr(deps.ErrOut)
	root.SetVersionTemplate("lazyset {{.Version}}\n")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError(err) })
	root.PersistentFlags().StringVar(&configPath, "config", "", "Configuration file (default: XDG config directory)")
	root.PersistentFlags().StringVar(&hostsPath, "hosts-config", "", "Machine-specific hosts file (default: hosts.toml beside the selected config)")
	root.Flags().StringVar(&opts.Host, "host", "", "Open the dashboard on this configured host")
	root.Flags().StringVar(&opts.Set, "set", "", "Browse this tool set without starting its members")
	root.Flags().StringVar(&opts.Tool, "tool", "", "Start one tool in Observe mode (alias for -- TOOL_ID)")
	root.Flags().StringSliceVar(&opts.StartSets, "start-set", nil, "Start embedded tools from these sets; repeat or separate IDs with commas")
	root.RunE = func(cmd *cobra.Command, args []string) error {
		for _, flag := range []string{"host", "set", "tool"} {
			if cmd.Flags().Changed(flag) && cmd.Flags().Lookup(flag).Value.String() == "" {
				return usageError(fmt.Errorf("--%s must not be empty", flag))
			}
		}
		if cmd.Flags().Changed("start-set") && len(opts.StartSets) == 0 {
			return usageError(errors.New("--start-set must not be empty"))
		}
		for _, id := range opts.StartSets {
			if strings.TrimSpace(id) == "" {
				return usageError(errors.New("--start-set must not contain empty IDs"))
			}
		}
		explicit := cmd.Flags().Changed("host") || cmd.Flags().Changed("set") || cmd.Flags().Changed("tool") || cmd.Flags().Changed("start-set") || len(args) > 0
		if !deps.IsTerminal(cmd.InOrStdin(), cmd.OutOrStdout()) {
			if explicit {
				return usageError(errors.New("dashboard selections and startup tools require terminal stdin and stdout; use tools list --host HOST --json for discovery"))
			}
			return cmd.Help()
		}
		cfg, sources, err := loadConfig(cmd, configPath, hostsPath)
		if err != nil {
			return err
		}
		if opts.Host != "" {
			if _, ok := cfg.Host(opts.Host); !ok {
				return usageError(fmt.Errorf("unknown host %q; configure it with config edit --hosts", opts.Host))
			}
		}
		if opts.Set != "" {
			if _, ok := cfg.Set(opts.Set); !ok {
				return usageError(fmt.Errorf("unknown set %q; see sets list", opts.Set))
			}
		}
		opts.Tools = append([]string(nil), args...)
		opts.Tools, opts.Warnings = tui.ResolveStartup(cfg, opts)
		opts.Tool, opts.StartSets = "", nil
		opts.Sources = sources
		writeWarnings(cmd.ErrOrStderr(), opts.Warnings)
		return deps.RunTUI(cfg, sources.MainPath, opts)
	}
	root.AddCommand(managedupgrade.NewCommand(managedupgrade.Product{Binary: "lazyset", Module: "github.com/daviddwlee84/lazyset", Main: "github.com/daviddwlee84/lazyset/cmd/lazyset"}))
	root.AddCommand(toolsCommand(deps, &configPath, &hostsPath), setsCommand(&configPath, &hostsPath), configCommand(deps, &configPath, &hostsPath))
	root.AddCommand(&cobra.Command{Use: "version", Short: "Print the build version", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "lazyset "+version)
		return err
	}})
	root.CompletionOptions.DisableDefaultCmd = true
	return root
}

func selectedSources(cmd *cobra.Command, mainPath, hostsPath string) (config.Sources, error) {
	return config.ResolveSources(mainPath, hostsPath,
		cmd.Root().PersistentFlags().Changed("config"), cmd.Root().PersistentFlags().Changed("hosts-config"))
}

func loadConfig(cmd *cobra.Command, mainPath, hostsPath string) (core.Config, config.Sources, error) {
	sources, err := selectedSources(cmd, mainPath, hostsPath)
	if err != nil {
		return core.Config{}, sources, usageError(err)
	}
	cfg, err := config.LoadSources(sources)
	if err != nil {
		return core.Config{}, sources, usageError(fmt.Errorf("%w; repair with config edit or config edit --hosts using the same configuration overrides", err))
	}
	writeWarnings(cmd.ErrOrStderr(), cfg.Warnings)
	return cfg, sources, nil
}

func writeWarnings(out io.Writer, warnings []string) {
	for _, warning := range warnings {
		fmt.Fprintln(out, "Warning:", humanText(warning))
	}
}

func group(use, short string) *cobra.Command {
	return &cobra.Command{Use: use, Short: short, Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
}

type toolRow struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	Mode        string   `json:"mode"`
	QToObserve  bool     `json:"q_to_observe"`
	ReturnKeys  []string `json:"return_keys"`
	QuitKeys    []string `json:"quit_keys,omitempty"`
	QuitHint    string   `json:"quit_hint,omitempty"`
	QuitSource  string   `json:"quit_source,omitempty"`
	State       string   `json:"state"`
	Path        string   `json:"path,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	InstallURL  string   `json:"install_url,omitempty"`
	Hint        string   `json:"hint,omitempty"`
}

type discoveryIssue struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}
type toolsOutput struct {
	Host  string          `json:"host"`
	Tools []toolRow       `json:"tools"`
	Error *discoveryIssue `json:"error,omitempty"`
}

func toolsCommand(deps Dependencies, configPath, hostsPath *string) *cobra.Command {
	group := group("tools", "Inspect the tool catalog and availability")
	var hostID string
	var installed, jsonOutput bool
	list := &cobra.Command{Use: "list", Short: "Discover catalog tools on one host without starting them", Args: noArgs,
		Example: "  lazyset tools list\n  lazyset tools list --host server --installed --json"}
	list.Flags().StringVar(&hostID, "host", "", "Configured host (default: default_host)")
	list.Flags().BoolVar(&installed, "installed", false, "Show only executables found on the host")
	list.Flags().BoolVar(&jsonOutput, "json", false, "Write JSON to stdout; diagnostics remain on stderr")
	list.RunE = func(cmd *cobra.Command, _ []string) error {
		cfg, _, err := loadConfig(cmd, *configPath, *hostsPath)
		if err != nil {
			return err
		}
		id := hostID
		if id == "" {
			id = cfg.DefaultHost
		}
		if cmd.Flags().Changed("host") && hostID == "" {
			return usageError(errors.New("--host must not be empty"))
		}
		h, ok := cfg.Host(id)
		if !ok {
			return usageError(fmt.Errorf("unknown host %q; configure it with config edit --hosts", id))
		}
		manager := deps.NewManager()
		observations, discoveryErr := manager.Discover(cmd.Context(), h, cfg.Tools)
		closeErr := manager.Close()
		discoveryErr = errors.Join(discoveryErr, closeErr)
		byID := make(map[string]core.Availability, len(observations))
		for _, observation := range observations {
			byID[observation.ToolID] = observation
		}
		result := toolsOutput{Host: id, Tools: []toolRow{}}
		unknown := 0
		for _, tool := range cfg.Tools {
			observation, ok := byID[tool.ID]
			if !ok {
				observation = core.Availability{State: "unknown", Reason: "discovery did not return an observation"}
			}
			if observation.State == "unknown" {
				unknown++
			}
			if installed && observation.State != "found" {
				continue
			}
			result.Tools = append(result.Tools, toolRow{
				ID: tool.ID, Name: tool.Name, Description: tool.Description, Category: tool.Category,
				Mode: tool.Mode, QToObserve: tool.QToObserve, ReturnKeys: tool.ReturnKeys,
				QuitKeys: tool.QuitKeys, QuitHint: tool.QuitHint, QuitSource: tool.QuitSource,
				State: observation.State, Path: observation.Path, Reason: observation.Reason,
				InstallURL: tool.InstallURL, Hint: tool.Hint,
			})
		}
		if discoveryErr == nil && unknown > 0 {
			discoveryErr = fmt.Errorf("discovery on host %s is incomplete: %d tool(s) have unknown availability", id, unknown)
		}
		if discoveryErr != nil {
			kind := host.Discovery
			var hostErr *host.Error
			if errors.As(discoveryErr, &hostErr) {
				kind = hostErr.Kind
			}
			result.Error = &discoveryIssue{Kind: kind, Message: discoveryErr.Error()}
		}
		if jsonOutput {
			if err := writeJSON(cmd.OutOrStdout(), result); err != nil {
				return err
			}
		} else {
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintf(w, "HOST\tSTATE\tTOOL\tPURPOSE / DETAIL\n")
			for _, row := range result.Tools {
				detail := humanText(row.Description)
				if row.Reason != "" {
					detail += " — " + humanText(row.Reason)
				}
				if row.Path != "" {
					detail += " [" + humanText(row.Path) + "]"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", humanText(id), humanText(row.State), humanText(row.ID), detail)
			}
			if err := w.Flush(); err != nil {
				return err
			}
		}
		return discoveryErr
	}
	group.AddCommand(list)
	return group
}

func setsCommand(configPath, hostsPath *string) *cobra.Command {
	group := group("sets", "Inspect built-in and custom tool sets")
	var jsonOutput bool
	list := &cobra.Command{Use: "list", Short: "List tool sets without discovery or connections", Args: noArgs}
	list.Flags().BoolVar(&jsonOutput, "json", false, "Write JSON")
	list.RunE = func(cmd *cobra.Command, _ []string) error {
		cfg, _, err := loadConfig(cmd, *configPath, *hostsPath)
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSON(cmd.OutOrStdout(), cfg.Sets)
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSOURCE\tTOOLS")
		for _, set := range cfg.Sets {
			kind := "custom"
			if set.Builtin {
				kind = "built-in"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", humanText(set.ID), humanText(set.Name), kind, humanText(strings.Join(set.Tools, ", ")))
		}
		return w.Flush()
	}
	group.AddCommand(list)
	return group
}

func configCommand(deps Dependencies, configPath, hostsPath *string) *cobra.Command {
	group := group("config", "Find, inspect, validate, or edit XDG configuration")
	var pathHosts bool
	pathCommand := &cobra.Command{Use: "path", Short: "Print the selected path without reading or creating it", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		sources, err := selectedSources(cmd, *configPath, *hostsPath)
		if err != nil {
			return usageError(err)
		}
		path := sources.MainPath
		if pathHosts {
			path = sources.HostsPath
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), path)
		return err
	}}
	pathCommand.Flags().BoolVar(&pathHosts, "hosts", false, "Print the machine-specific hosts file path")
	group.AddCommand(pathCommand)
	var jsonOutput bool
	show := &cobra.Command{Use: "show", Short: "Show effective defaults and overrides with all environment values redacted", Args: noArgs}
	show.Flags().BoolVar(&jsonOutput, "json", false, "Write JSON without a heading")
	show.RunE = func(cmd *cobra.Command, _ []string) error {
		cfg, sources, err := loadConfig(cmd, *configPath, *hostsPath)
		if err != nil {
			return err
		}
		redactEnv(&cfg)
		if !jsonOutput {
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), "Effective configuration (environment values redacted):"); err != nil {
				return err
			}
		}
		return writeJSON(cmd.OutOrStdout(), struct {
			Path      string      `json:"path"`
			HostsPath string      `json:"hosts_path"`
			Config    core.Config `json:"config"`
		}{sources.MainPath, sources.HostsPath, cfg})
	}
	group.AddCommand(show)
	group.AddCommand(&cobra.Command{Use: "validate", Short: "Validate the effective configuration and its references", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		_, sources, err := loadConfig(cmd, *configPath, *hostsPath)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Effective configuration is valid:\n  config: %s\n  hosts: %s\n", humanText(sources.MainPath), humanText(sources.HostsPath))
		return err
	}})
	var editHosts bool
	edit := &cobra.Command{Use: "edit", Short: "Edit using VISUAL, EDITOR, or vi, then validate both configuration files", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !deps.IsTerminal(cmd.InOrStdin(), cmd.OutOrStdout()) {
			return usageError(errors.New("config edit requires terminal stdin and stdout; use config path or config path --hosts to locate the file"))
		}
		sources, err := selectedSources(cmd, *configPath, *hostsPath)
		if err != nil {
			return usageError(err)
		}
		path := sources.MainPath
		prepareEditor := config.EditCommand
		if editHosts {
			path, prepareEditor = sources.HostsPath, config.EditHostsCommand
			sources.HostsExplicit = true
		} else {
			sources.MainExplicit = true
		}
		editor, err := prepareEditor(path)
		if err != nil {
			return err
		}
		editor.Stdin = cmd.InOrStdin()
		editor.Stdout = cmd.OutOrStdout()
		editor.Stderr = cmd.ErrOrStderr()
		if err := deps.RunEditor(editor); err != nil {
			wrapped := fmt.Errorf("editor: %w", err)
			var child *exec.ExitError
			if errors.As(err, &child) {
				code := child.ExitCode()
				if code < 1 {
					code = 130
				}
				return &commandError{code, wrapped}
			}
			return wrapped
		}
		cfg, err := config.LoadSources(sources)
		if err != nil {
			return usageError(fmt.Errorf("edited file was preserved but the combined configuration is not valid: %w; repair with config edit or config edit --hosts using the same configuration overrides", err))
		}
		writeWarnings(cmd.ErrOrStderr(), cfg.Warnings)
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "Configuration is valid:", humanText(path))
		return err
	}}
	edit.Flags().BoolVar(&editHosts, "hosts", false, "Edit the machine-specific hosts file")
	group.AddCommand(edit)
	return group
}

func redactEnv(cfg *core.Config) {
	for i := range cfg.Hosts {
		if cfg.Hosts[i].Env != nil {
			redacted := map[string]string{}
			for key := range cfg.Hosts[i].Env {
				redacted[key] = "<redacted>"
			}
			cfg.Hosts[i].Env = redacted
		}
	}
	for i := range cfg.Tools {
		if cfg.Tools[i].Env != nil {
			redacted := map[string]string{}
			for key := range cfg.Tools[i].Env {
				redacted[key] = "<redacted>"
			}
			cfg.Tools[i].Env = redacted
		}
	}
}

func writeJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

// humanText keeps external text inside a single display field. Newlines and
// tabs come only from our layout; ANSI/OSC sequences cannot control a terminal.
// Machine JSON intentionally retains the original strings through JSON escaping.
func humanText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, ansi.Strip(value))
}

// Run prints diagnostics once and returns an exit code after cleanup.
func Run(args []string, version string, deps Dependencies) int {
	deps = deps.defaults()
	cmd := NewRoot(version, deps)
	cmd.SetArgs(args)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	cmd.SetContext(ctx)
	err := cmd.Execute()
	if err != nil {
		fmt.Fprintln(deps.ErrOut, "Error:", humanText(err.Error()))
	}
	return ExitCode(err)
}

func Execute(version string) int { return Run(os.Args[1:], version, Dependencies{}) }
