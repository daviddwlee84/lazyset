package catalog

import "slices"

// Native quit bindings are documentation, not a list of safe keys to intercept:
// several also close child dialogs or cancel work. Only a native lowercase q is
// protected by default. Explicit return_keys remain a complete override.
func keyDefaults(id string) (returns, quits []string, hint string) {
	switch id {
	case "btop", "top", "ncdu", "bandwhich":
		quits = []string{"q"}
	case "htop":
		quits = []string{"q", "f10"}
	case "bottom", "gdu", "lazygit":
		quits = []string{"q", "Q", "ctrl+c"}
	case "glances", "dev":
		quits = []string{"q", "esc", "ctrl+c"}
	case "nvtop":
		quits = []string{"q", "esc", "f10"}
	case "nvitop", "lnav", "yazi":
		quits = []string{"q", "Q"}
	case "superfile":
		quits = []string{"q", "esc", "Q"}
	case "lazydocker", "lazychezmoi", "lazyclash", "lazymlflow", "lazypueue", "lazycrontab", "exp":
		quits = []string{"q", "ctrl+c"}
	case "translate":
		quits = []string{"ctrl+c", "esc"}
	case "k9s":
		quits = []string{"ctrl+c", ":q", ":quit"}
	}
	if slices.Contains(quits, "q") {
		returns = []string{"q"}
	}
	switch id {
	case "k9s":
		hint = "Ctrl+C and the native :q and :quit commands can exit K9s."
	case "gdu":
		hint = "Ctrl+C can cancel a scan; it is passed through by default."
	case "dev":
		hint = "Esc clears filters or closes dialogs; Ctrl+C can cancel a clone. Both pass through by default."
	case "translate":
		hint = "Esc also closes history, language, and model overlays. No return keys are protected by default."
	case "lazychezmoi":
		hint = "Ctrl+C can close child dialogs; it is passed through by default."
	case "lazypueue":
		hint = "q, Esc, and Ctrl+C can close a log view; Ctrl+C can cancel an upgrade wait. A guarded q also applies in child views."
	case "superfile":
		hint = "Esc also closes child views; Ctrl+C copies items or cancels typing. Custom and Vim keymaps can differ. Q's shell directory handoff cannot change lazyset's working directory."
	default:
		hint = "Native q may also close child views or be ordinary text in prompts."
	}
	return
}
