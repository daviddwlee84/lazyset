package catalog

import "lazyset/internal/core"

// personalTools contains the independently installed personal CLI suite. Each
// command opens its existing TUI; discovering it does not configure its backend.
func personalTools() []core.Tool {
	const category = "Personal tools"
	return []core.Tool{
		tool("dev", "dev-cli", "Browse repositories, tasks, worktrees, fleet, and SSH hosts", category, []string{"dev", "tui"}, "https://github.com/daviddwlee84/dev-cli", "The executable is dev. Runtime and shell handoffs retain dev's own behavior; they cannot change lazyset's parent shell."),
		tool("lazychezmoi", "lazychezmoi", "Inspect, edit, preview, and apply chezmoi dotfiles", category, []string{"lazychezmoi", "tui"}, "https://github.com/daviddwlee84/lazychezmoi", "Requires chezmoi. Uses its configured source and destination; shell reload cannot change lazyset's parent shell."),
		tool("lazyclash", "lazyclash", "Manage Mihomo proxy targets, connections, and routing", category, []string{"lazyclash"}, "https://github.com/daviddwlee84/lazyclash", "Uses configured Mihomo controllers. Installing this CLI does not install or configure a proxy backend."),
		tool("lazymlflow", "lazymlflow", "Browse MLflow experiments, compare runs, and inspect artifacts", category, []string{"lazymlflow"}, "https://github.com/daviddwlee84/lazymlflow", "Uses configured tracking targets. Local stores can prepare Python dependencies and start an owned temporary official MLflow server."),
		tool("lazypueue", "lazypueue", "Browse Pueue queues, manage jobs, and follow task logs", category, []string{"lazypueue"}, "https://github.com/daviddwlee84/lazypueue", "Requires Pueue and an existing daemon on each queue host; the dashboard does not start daemons automatically."),
		tool("lazycrontab", "lazycrontab", "Understand cron schedules and review local or SSH job changes", category, []string{"lazycrontab"}, "https://github.com/daviddwlee84/lazycrontab", "Uses existing crontab tools and configured sources. Opening the dashboard does not install a scheduler or run jobs."),
		tool("translate", "translate", "Translate text interactively and look up words", category, []string{"translate"}, "https://github.com/daviddwlee84/translate", "Uses configured translation providers and dictionaries. Input stays with the translator; use the host prefix to return."),
		tool("exp", "exp", "Inspect a local Git-based research workflow and its evidence", category, []string{"exp", "ui"}, "https://github.com/daviddwlee84/exp-cli", "The read-only UI uses the current research project or configured workspace; set dir for a specific project."),
	}
}

// PersonalSet groups the personal TUIs without requiring every one to be
// installed or launching any of them when the set is selected.
func PersonalSet() core.Set {
	tools := personalTools()
	ids := make([]string, len(tools))
	for i, t := range tools {
		ids[i] = t.ID
	}
	return core.Set{ID: "personal", Name: "Personal tools", Tools: ids, Builtin: true}
}
