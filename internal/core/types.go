// Package core contains the shared, effective configuration and discovery types.
package core

type Tool struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Category    string            `json:"category"`
	Command     []string          `json:"command"`
	Candidates  []string          `json:"candidates,omitempty"`
	Platforms   []string          `json:"platforms,omitempty"`
	Mode        string            `json:"mode"`
	QToObserve  bool              `json:"q_to_observe"`
	ReturnKeys  []string          `json:"return_keys"`
	QuitKeys    []string          `json:"quit_keys,omitempty"`
	QuitHint    string            `json:"quit_hint,omitempty"`
	QuitSource  string            `json:"quit_source,omitempty"`
	Dir         string            `json:"dir,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	InstallURL  string            `json:"install_url,omitempty"`
	Hint        string            `json:"hint,omitempty"`
}

type Host struct {
	ID   string            `json:"id"`
	Name string            `json:"name"`
	SSH  string            `json:"ssh,omitempty"`
	Env  map[string]string `json:"env,omitempty"`
}

type Set struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Tools   []string `json:"tools"`
	Builtin bool     `json:"builtin"`
}

type Config struct {
	Prefix      string   `json:"prefix"`
	Mouse       bool     `json:"mouse"`
	FocusClick  string   `json:"focus_click"`
	DefaultHost string   `json:"default_host"`
	DefaultSet  string   `json:"default_set"`
	Hosts       []Host   `json:"hosts"`
	Tools       []Tool   `json:"tools"`
	Sets        []Set    `json:"sets"`
	Warnings    []string `json:"-"`
}

type Availability struct {
	ToolID string `json:"tool_id"`
	State  string `json:"state"` // found, missing, unsupported, unknown
	Path   string `json:"path,omitempty"`
	Reason string `json:"reason,omitempty"`
}

func (c Config) Tool(id string) (Tool, bool) {
	for _, t := range c.Tools {
		if t.ID == id {
			return t, true
		}
	}
	return Tool{}, false
}
func (c Config) Host(id string) (Host, bool) {
	for _, h := range c.Hosts {
		if h.ID == id {
			return h, true
		}
	}
	return Host{}, false
}
func (c Config) Set(id string) (Set, bool) {
	for _, s := range c.Sets {
		if s.ID == id {
			return s, true
		}
	}
	return Set{}, false
}
