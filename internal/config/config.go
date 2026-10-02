// Package config loads pocketagent's YAML config file.
//
// The config knows the shape shared by every agent and speech provider, but
// not their individual settings: those are kept as Options and decoded by
// the implementation that owns them.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
)

type Config struct {
	Telegram    Telegram           `yaml:"telegram"`
	Defaults    Defaults           `yaml:"defaults"`
	Permissions Permissions        `yaml:"permissions"`
	Agents      map[string]Agent   `yaml:"agents"`
	Projects    map[string]Project `yaml:"projects"`
	Transcriber Provider           `yaml:"transcriber"`
	TTS         Provider           `yaml:"tts"`
	Budget      Budget             `yaml:"budget"`
	Output      Output             `yaml:"output"`
	Checkpoints *bool              `yaml:"checkpoints"`
	// BridgeHost is how agents reach pocketagent's tool server. Set it to
	// host.docker.internal when agents run in containers.
	BridgeHost string `yaml:"bridge_host"`

	// Home holds state, logs and uploads: the directory of the config file.
	Home string `yaml:"-"`
	Path string `yaml:"-"`
}

type Telegram struct {
	Token        string  `yaml:"token"`
	AllowedUsers []int64 `yaml:"allowed_users"`
}

type Defaults struct {
	Agent string     `yaml:"agent"`
	Cwd   string     `yaml:"cwd"`
	Mode  agent.Mode `yaml:"mode"`
}

type Permissions struct {
	// AutoAllow lists tool kinds that run without asking.
	AutoAllow []agent.Kind `yaml:"auto_allow"`
	Timeout   Duration     `yaml:"timeout"`
}

// Agent is one configured agent. Keys other than the common ones below are
// the adapter's own options.
type Agent struct {
	Type    string   `yaml:"type"`
	Command Command  `yaml:"command"`
	Wrap    Command  `yaml:"wrap"`
	Env     []string `yaml:"env"`
	Model   string   `yaml:"model"`  // default model
	Models  []string `yaml:"models"` // offered in the model picker

	Options Options `yaml:"-"`
}

// Spec converts the config entry into what an adapter is built from.
func (a Agent) Spec(name string) agent.Spec {
	return agent.Spec{Name: name, Command: a.Command, Wrap: a.Wrap, Env: a.Env, Options: a.Options}
}

func (a *Agent) UnmarshalYAML(n *yaml.Node) error {
	type plain Agent
	opts, err := splitOptions(n, (*plain)(a))
	a.Options = opts
	return err
}

// Provider selects a speech implementation by type; every other key is the
// provider's own options.
type Provider struct {
	Type    string  `yaml:"type"`
	Options Options `yaml:"-"`
}

func (p *Provider) UnmarshalYAML(n *yaml.Node) error {
	type plain Provider
	opts, err := splitOptions(n, (*plain)(p))
	p.Options = opts
	return err
}

type Project struct {
	Cwd   string     `yaml:"cwd"`
	Agent string     `yaml:"agent"`
	Model string     `yaml:"model"`
	Mode  agent.Mode `yaml:"mode"`
}

type Budget struct {
	DailyUSD float64 `yaml:"daily_usd"` // per user; 0 means unlimited
}

type Output struct {
	// Replies longer than this many characters are sent as a file.
	FileThreshold int `yaml:"file_threshold"`
}

// ---------- types ----------

// Options holds implementation-specific keys from a YAML mapping.
type Options struct{ node *yaml.Node }

// Decode fills v (a pointer to a struct with yaml tags), rejecting unknown keys.
func (o Options) Decode(v any) error {
	if o.node == nil {
		return nil
	}
	data, err := yaml.Marshal(o.node)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("line %d: %w", o.node.Line, err)
	}
	return nil
}

// OptionsFrom builds Options from a map, for tests and generated configs.
func OptionsFrom(m map[string]any) Options {
	var n yaml.Node
	n.Encode(m)
	return Options{node: &n}
}

// splitOptions decodes the keys of n that are fields of common (by yaml
// tag) into common and returns the rest as Options.
func splitOptions(n *yaml.Node, common any) (Options, error) {
	if n.Kind != yaml.MappingNode {
		return Options{}, fmt.Errorf("line %d: expected a mapping", n.Line)
	}
	known := yamlKeys(common)
	commonNode := &yaml.Node{Kind: yaml.MappingNode, Line: n.Line}
	rest := &yaml.Node{Kind: yaml.MappingNode, Line: n.Line}
	for i := 0; i+1 < len(n.Content); i += 2 {
		dst := rest
		if slices.Contains(known, n.Content[i].Value) {
			dst = commonNode
		}
		dst.Content = append(dst.Content, n.Content[i], n.Content[i+1])
	}
	if err := commonNode.Decode(common); err != nil {
		return Options{}, err
	}
	return Options{node: rest}, nil
}

// Command accepts either a string ("aider --yes") or a list (["npx", "-y", "pkg"]).
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

// ---------- loading ----------

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
	return Parse(raw, path)
}

// Parse reads a config from YAML; path determines Home.
func Parse(raw []byte, path string) (*Config, error) {
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
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func (c *Config) applyDefaults() {
	if c.Defaults.Cwd == "" {
		c.Defaults.Cwd, _ = os.UserHomeDir()
	}
	c.Defaults.Cwd = ExpandHome(c.Defaults.Cwd)
	if c.Defaults.Mode == "" {
		c.Defaults.Mode = agent.ModeAsk
	}
	if c.Defaults.Agent == "" && len(c.Agents) > 0 {
		c.Defaults.Agent = c.AgentNames()[0]
	}
	if c.Permissions.AutoAllow == nil {
		c.Permissions.AutoAllow = []agent.Kind{agent.KindRead, agent.KindSearch, agent.KindThink}
	}
	if c.Permissions.Timeout == 0 {
		c.Permissions.Timeout = Duration(10 * time.Minute)
	}
	for name, p := range c.Projects {
		p.Cwd = ExpandHome(p.Cwd)
		c.Projects[name] = p
	}
	if c.Transcriber.Type == "" {
		c.Transcriber.Type = "none"
	}
	if c.TTS.Type == "" {
		c.TTS.Type = "none"
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
		return fmt.Errorf("telegram.allowed_users is required (run `pocketagent init`)")
	}
	if len(c.Agents) == 0 {
		return fmt.Errorf("configure at least one agent under agents: (run `pocketagent init`)")
	}
	for name, a := range c.Agents {
		if a.Type == "" {
			return fmt.Errorf("agents.%s.type is required", name)
		}
		if len(a.Command) == 0 {
			return fmt.Errorf("agents.%s.command is required", name)
		}
	}
	if _, ok := c.Agents[c.Defaults.Agent]; !ok {
		return fmt.Errorf("defaults.agent %q is not in agents", c.Defaults.Agent)
	}
	if err := validMode(c.Defaults.Mode); err != nil {
		return fmt.Errorf("defaults.mode: %w", err)
	}
	for _, k := range c.Permissions.AutoAllow {
		if k == agent.KindExecute || k == agent.KindDelete {
			return fmt.Errorf("permissions.auto_allow: %q is too broad; use defaults.mode: full instead", k)
		}
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
		if p.Mode != "" {
			if err := validMode(p.Mode); err != nil {
				return fmt.Errorf("projects.%s.mode: %w", name, err)
			}
		}
	}
	return nil
}

func validMode(m agent.Mode) error {
	if !slices.Contains(agent.Modes, m) {
		return fmt.Errorf("%q is not one of %v", m, agent.Modes)
	}
	return nil
}

// AgentNames returns configured agent names, sorted.
func (c *Config) AgentNames() []string { return sortedKeys(c.Agents) }

// ProjectNames returns configured project names, sorted.
func (c *Config) ProjectNames() []string { return sortedKeys(c.Projects) }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// CheckpointsEnabled reports whether git checkpoints are on (default true).
func (c *Config) CheckpointsEnabled() bool { return c.Checkpoints == nil || *c.Checkpoints }

// ExpandHome replaces a leading ~ with the user's home directory.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return p
}

// yamlKeys lists the yaml keys of a struct pointer's fields.
func yamlKeys(v any) []string {
	t := reflect.TypeOf(v).Elem()
	var keys []string
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		if name != "" && name != "-" {
			keys = append(keys, name)
		}
	}
	return keys
}
