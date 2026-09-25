// Package catalog supplies the small, curated collection of known TUIs. Being
// listed here is not a claim that a tool is installed or ready on any host.
package catalog

import (
	"github.com/daviddwlee84/lazyset/internal/core"
	"slices"
)

// Tools returns a fresh catalog, safe for callers to customize.
func Tools() []core.Tool {
	return append([]core.Tool{
		tool("btop", "btop", "CPU, memory, disks, network, and processes at a glance", "System", []string{"btop"}, "https://github.com/aristocratos/btop", ""),
		tool("htop", "htop", "Browse, filter, and inspect running processes", "System", []string{"htop"}, "https://htop.dev/", "Process actions can change or terminate processes."),
		tool("top", "top", "The system's built-in process and resource monitor", "System", []string{"top"}, "https://gitlab.com/procps-ng/procps", "Usually included with the operating system; behavior differs between macOS and Linux."),
		tool("bottom", "bottom", "Graphical resource and process monitoring", "System", []string{"btm"}, "https://github.com/ClementTsang/bottom", "The executable is named btm."),
		tool("glances", "Glances", "A broad overview of system resources and processes", "System", []string{"glances"}, "https://github.com/nicolargo/glances", "Optional integrations depend on installed plugins and services."),
		tool("nvtop", "nvtop", "GPU utilization, memory, and GPU processes", "GPU", []string{"nvtop"}, "https://github.com/Syllo/nvtop", "Available metrics depend on GPU hardware, drivers, and the tool's build."),
		tool("nvitop", "nvitop", "NVIDIA GPU and process monitoring", "GPU", []string{"nvitop"}, "https://github.com/XuehaiPan/nvitop", "Requires a working NVIDIA driver and supported GPU."),
		tool("gdu", "gdu", "Browse disk usage and find large directories", "Disk", []string{"gdu"}, "https://github.com/dundee/gdu", "Homebrew names this gdu-go; GNU coreutils gdu is a different program. File deletion is available inside the tool."),
		tool("ncdu", "ncdu", "Scan and navigate directory sizes", "Disk", []string{"ncdu"}, "https://dev.yorhel.nl/ncdu", "Scans the current directory by default; file deletion is available inside the tool."),
		tool("bandwhich", "bandwhich", "Inspect bandwidth by process and connection", "Network", []string{"bandwhich"}, "https://github.com/imsnif/bandwhich", "Packet capture may require privileges; lazyset never silently adds sudo."),
		tool("lazydocker", "lazydocker", "Inspect and manage Docker containers and logs", "Containers", []string{"lazydocker"}, "https://github.com/jesseduffield/lazydocker", "Requires access to a Docker daemon. Management actions affect the selected containers."),
		tool("k9s", "K9s", "Explore and manage Kubernetes workloads", "Containers", []string{"k9s"}, "https://k9scli.io/", "Uses your Kubernetes context. Native Ctrl+C and :q / :quit commands remain available."),
		tool("lnav", "lnav", "Browse, search, and analyze logs", "Logs", []string{"lnav"}, "https://lnav.org/", "Supply log paths in a custom command when the default logs are unsuitable."),
		tool("lazygit", "lazygit", "Explore and manage changes in a Git repository", "Files / Git", []string{"lazygit"}, "https://github.com/jesseduffield/lazygit", "Starts in the current directory; requires a Git repository."),
		tool("yazi", "Yazi", "Browse files and launch their associated tools", "Files / Git", []string{"yazi"}, "https://yazi-rs.github.io/", "Starts in this host's home directory. Image previews and external openers depend on terminal capabilities; external mode is available."),
		tool("superfile", "Superfile", "Browse and manage files across multiple panels", "Files / Git", []string{"spf"}, "https://superfile.dev/", "The executable is spf. Starts in this host's home directory. Previews and external openers depend on terminal capabilities; external mode is available."),
	}, personalTools()...)
}

func tool(id, name, description, category string, command []string, installURL, hint string) core.Tool {
	t := core.Tool{ID: id, Name: name, Description: description, Category: category, Command: command, Mode: "embedded", InstallURL: installURL, Hint: hint}
	t.ReturnKeys, t.QuitKeys, t.QuitHint = keyDefaults(id)
	t.QToObserve = slices.Contains(t.ReturnKeys, "q")
	t.QuitSource = installURL
	if id == "gdu" {
		t.Candidates = []string{"gdu-go", "gdu"}
	}
	if id == "yazi" || id == "superfile" {
		t.Dir = "~"
	}
	if id == "superfile" {
		t.QuitSource = "https://superfile.dev/list/hotkey-list/"
	}
	return t
}

const AllSetID = "all"

// AllSet includes every effective tool in catalog order, including custom tools.
// A copied set keeps this snapshot rather than following later catalog changes.
func AllSet(tools []core.Tool) core.Set {
	ids := make([]string, 0, len(tools))
	for _, tool := range tools {
		ids = append(ids, tool.ID)
	}
	return core.Set{ID: AllSetID, Name: "All", Tools: ids, Builtin: true}
}

// Sets are suggestions, not requirements or batches of eagerly started tools.
func Sets() []core.Set {
	return []core.Set{
		AllSet(Tools()),
		{ID: "system", Name: "System", Tools: []string{"btop", "htop", "top", "bottom", "glances"}, Builtin: true},
		{ID: "gpu", Name: "GPU", Tools: []string{"nvtop", "nvitop", "btop"}, Builtin: true},
		{ID: "disk", Name: "Disk", Tools: []string{"gdu", "ncdu", "btop"}, Builtin: true},
		{ID: "network", Name: "Network", Tools: []string{"bandwhich", "btop"}, Builtin: true},
		{ID: "containers", Name: "Containers", Tools: []string{"lazydocker", "k9s"}, Builtin: true},
		{ID: "logs", Name: "Logs", Tools: []string{"lnav"}, Builtin: true},
		{ID: "files-git", Name: "Files / Git", Tools: []string{"yazi", "superfile", "lazygit"}, Builtin: true},
		PersonalSet(),
	}
}

func IsBuiltinSet(id string) bool {
	for _, s := range Sets() {
		if s.ID == id {
			return true
		}
	}
	return false
}
