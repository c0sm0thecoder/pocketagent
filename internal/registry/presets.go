package registry

// Preset is a known agent that `init` can detect and configure. Presets are
// data, sorted by name; none is preferred over another.
type Preset struct {
	Name     string
	Requires []string // executables that must be on PATH
	Config   string   // YAML body for the agent entry
	Login    string   // how to log in, shown by doctor
}

var Presets = []Preset{
	{
		Name:     "aider",
		Requires: []string{"aider"},
		Config: `type: command
command: [aider, --message, "{prompt}", --yes-always, --no-pretty, --no-stream]
model_args: [--model, "{model}"]
image_args: ["{path}"]
timeout: 30m`,
		Login: "aider",
	},
	{
		Name:     "claude-code",
		Requires: []string{"claude"},
		Config: `type: claude-code
command: claude
models: [opus, sonnet, haiku]`,
		Login: "claude",
	},
	{
		Name:     "codex",
		Requires: []string{"codex", "npx"},
		Config: `type: acp
command: [npx, -y, "@zed-industries/codex-acp"]`,
		Login: "codex login",
	},
	{Name: "copilot", Requires: []string{"copilot"}, Config: "type: acp\ncommand: [copilot, --acp, --stdio]", Login: "copilot"},
	{Name: "cursor", Requires: []string{"cursor-agent"}, Config: "type: acp\ncommand: [cursor-agent, acp]", Login: "cursor-agent login"},
	{Name: "gemini", Requires: []string{"gemini"}, Config: "type: acp\ncommand: [gemini, --acp]", Login: "gemini"},
	{Name: "goose", Requires: []string{"goose"}, Config: "type: acp\ncommand: [goose, acp]", Login: "goose configure"},
	{Name: "kiro", Requires: []string{"kiro-cli"}, Config: "type: acp\ncommand: [kiro-cli, acp]", Login: "kiro-cli login"},
	{Name: "opencode", Requires: []string{"opencode"}, Config: "type: acp\ncommand: [opencode, acp]", Login: "opencode auth login"},
}

// FindPreset returns the preset with the given name.
func FindPreset(name string) (Preset, bool) {
	for _, p := range Presets {
		if p.Name == name {
			return p, true
		}
	}
	return Preset{}, false
}
