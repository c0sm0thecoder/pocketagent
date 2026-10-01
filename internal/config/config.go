// Package config loads pocketagent's YAML config file.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type Config struct {
	Telegram        Telegram           `yaml:"telegram"`
	Defaults        Defaults           `yaml:"defaults"`
	AutoAllow       []string           `yaml:"auto_allow"`
	ApprovalTimeout Duration           `yaml:"approval_timeout"`
	Agents          map[string]Agent   `yaml:"agents"`
	Projects        map[string]Project `yaml:"projects"`
	Transcriber     Transcriber        `yaml:"transcriber"`
	TTS             TTS                `yaml:"tts"`
	Budget          Budget             `yaml:"budget"`
	Output          Output             `yaml:"output"`
	Checkpoints     *bool              `yaml:"checkpoints"`
	// BridgeHost is how agents reach pocketagent's MCP server. Set it to
	// host.docker.internal when agents run in containers.
	BridgeHost string `yaml:"bridge_host"`

	// Home is the directory holding state, logs and uploads. Not read from YAML.
	Home string `yaml:"-"`
	// Path is the file this config was loaded from.
	Path string `yaml:"-"`
}

type Telegram struct {
	Token        string  `yaml:"token"`
	AllowedUsers []int64 `yaml:"allowed_users"`
}

type Defaults struct {
	Agent string `yaml:"agent"`
	Cwd   string `yaml:"cwd"`
	Mode  string `yaml:"mode"`
}

type Agent struct {
	// Type is "claude" (native Claude Code CLI), "acp" (any Agent Client
	// Protocol agent) or "command" (any CLI that takes a prompt and prints a reply).
	Type    string   `yaml:"type"`
	Command Command  `yaml:"command"`
	Env     []string `yaml:"env"`
	Models  []string `yaml:"models"`
	Model   string   `yaml:"model"`

	// Wrap is prepended to the agent command, e.g. a docker run invocation
	// for sandboxing. Supports {cwd}.
	Wrap Command `yaml:"wrap"`

	// command type only
	ModelArgs Command  `yaml:"model_args"` // added when a model is set; supports {model}
	ImageArgs Command  `yaml:"image_args"` // added once per image; supports {path}
	Stdin     bool     `yaml:"stdin"`      // send the prompt on stdin instead of {prompt}
	Timeout   Duration `yaml:"timeout"`
}

type Project struct {
	Cwd   string `yaml:"cwd"`
	Agent string `yaml:"agent"`
	Model string `yaml:"model"`
	Mode  string `yaml:"mode"`
}

type Transcriber struct {
	// Type is "whisper-cpp", "openai" (any OpenAI-compatible endpoint),
	// "command" or "none".
	Type     string  `yaml:"type"`
	Command  Command `yaml:"command"` // whisper-cpp binary, or the command template ({file})
	Model    string  `yaml:"model"`
	Language string  `yaml:"language"`
	BaseURL  string  `yaml:"base_url"`
	APIKey   string  `yaml:"api_key"`
	FFmpeg   string  `yaml:"ffmpeg"`
}

type TTS struct {
	// Type is "say" (macOS), "openai" (OpenAI-compatible /audio/speech),
	// "command" or "none".
	Type    string  `yaml:"type"`
	Command Command `yaml:"command"` // command template ({text_file}, {out})
	Model   string  `yaml:"model"`
	Voice   string  `yaml:"voice"`
	BaseURL string  `yaml:"base_url"`
	APIKey  string  `yaml:"api_key"`
	FFmpeg  string  `yaml:"ffmpeg"`
}

type Budget struct {
	DailyUSD float64 `yaml:"daily_usd"` // per user; 0 means unlimited
}

type Output struct {
	// Replies longer than this many characters are sent as a .md file.
	FileThreshold int `yaml:"file_threshold"`
}

// Modes control how much the agent may do without asking.
var Modes = []string{"ask", "edits", "plan", "yolo"}

// Command accepts either a string ("claude") or a list (["npx", "-y", "pkg"]).
type Command []string

func (c *Command) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*c = strings.Fields(n.Value)
		return nil
	}
	var list []string
	if err := n.Decode(&list); err != nil {
		return err
	}
	*c = list
	return nil
}

// Duration accepts Go duration strings like "10m".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) D() time.Duration { return time.Duration(d) }

// DefaultHome is $POCKETAGENT_HOME or ~/.pocketagent.
func DefaultHome() string {
	if h := os.Getenv("POCKETAGENT_HOME"); h != "" {
		return ExpandHome(h)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pocketagent")
}

// DefaultPath is the config file inside DefaultHome.
func DefaultPath() string { return filepath.Join(DefaultHome(), "config.yaml") }

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Load reads and validates the config at path. ${VAR} references are
// replaced with environment variables, so secrets can live outside the file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	expanded := envRef.ReplaceAllStringFunc(string(raw), func(m string) string {
		return os.Getenv(envRef.FindStringSubmatch(m)[1])
	})

	c := &Config{}
	dec := yaml.NewDecoder(strings.NewReader(expanded))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.Path = path
	c.Home = filepath.Dir(path)
	c.applyDefaults()
	return c, c.validate()
}

func (c *Config) applyDefaults() {
	if c.Defaults.Cwd == "" {
		c.Defaults.Cwd, _ = os.UserHomeDir()
	}
	c.Defaults.Cwd = ExpandHome(c.Defaults.Cwd)
	if c.Defaults.Mode == "" {
		c.Defaults.Mode = "ask"
	}
	if c.AutoAllow == nil {
		c.AutoAllow = []string{"read", "search", "think"}
	}
	if c.ApprovalTimeout == 0 {
		c.ApprovalTimeout = Duration(10 * time.Minute)
	}
	if len(c.Agents) == 0 {
		c.Agents = map[string]Agent{"claude": {Type: "claude", Command: Command{"claude"}, Models: []string{"opus", "sonnet", "haiku"}}}
	}
	if c.Defaults.Agent == "" {
		names := c.AgentNames()
		c.Defaults.Agent = names[0]
	}
	for name, p := range c.Projects {
		p.Cwd = ExpandHome(p.Cwd)
		c.Projects[name] = p
	}
	if c.Transcriber.Type == "" {
		c.Transcriber.Type = "none"
	}
	if c.Transcriber.Language == "" {
		c.Transcriber.Language = "auto"
	}
	c.Transcriber.Model = ExpandHome(c.Transcriber.Model)
	if c.Transcriber.FFmpeg == "" {
		c.Transcriber.FFmpeg = "ffmpeg"
	}
	if c.TTS.Type == "" {
		c.TTS.Type = "none"
	}
	if c.TTS.FFmpeg == "" {
		c.TTS.FFmpeg = "ffmpeg"
	}
	if c.Output.FileThreshold == 0 {
		c.Output.FileThreshold = 8000
	}
}

func (c *Config) validate() error {
	if c.Telegram.Token == "" {
		return fmt.Errorf("telegram.token is required")
	}
	// The bot runs code on this machine, so it never starts without an allowlist.
	if len(c.Telegram.AllowedUsers) == 0 {
		return fmt.Errorf("telegram.allowed_users is required (run `pocketagent init`, or message @userinfobot for your id)")
	}
	for name, a := range c.Agents {
		switch a.Type {
		case "claude", "acp", "command":
		default:
			return fmt.Errorf("agents.%s.type must be claude, acp or command (got %q)", name, a.Type)
		}
		if len(a.Command) == 0 && a.Type != "claude" {
			return fmt.Errorf("agents.%s.command is required", name)
		}
	}
	if _, ok := c.Agents[c.Defaults.Agent]; !ok {
		return fmt.Errorf("defaults.agent %q is not in agents", c.Defaults.Agent)
	}
	if !slices.Contains(Modes, c.Defaults.Mode) {
		return fmt.Errorf("defaults.mode must be one of %v", Modes)
	}
	for name, p := range c.Projects {
		if p.Cwd == "" {
			return fmt.Errorf("projects.%s.cwd is required", name)
		}
		if p.Agent != "" {
			if _, ok := c.Agents[p.Agent]; !ok {
				return fmt.Errorf("projects.%s.agent %q is not in agents", name, p.Agent)
			}
		}
	}
	return nil
}

// AgentNames returns configured agent names, sorted.
func (c *Config) AgentNames() []string {
	var names []string
	for n := range c.Agents {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// ProjectNames returns configured project names, sorted.
func (c *Config) ProjectNames() []string {
	var names []string
	for n := range c.Projects {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// CheckpointsEnabled reports whether git checkpoints are on (default true).
func (c *Config) CheckpointsEnabled() bool { return c.Checkpoints == nil || *c.Checkpoints }

func (c *Config) IsAllowed(userID int64) bool {
	return slices.Contains(c.Telegram.AllowedUsers, userID)
}

func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return p
}

// Expand replaces {key} placeholders in each argument.
func Expand(args []string, vars map[string]string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		for k, v := range vars {
			a = strings.ReplaceAll(a, "{"+k+"}", v)
		}
		out[i] = a
	}
	return out
}
