package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/c0sm0thecoder/pocketagent/internal/config"
)

// knownAgent is an agent init can detect and doctor can recognize.
type knownAgent struct {
	name  string
	bins  []string // all must be on PATH
	yaml  string   // config body, indented under the agent name
	login string   // how to log in, for doctor hints
}

var knownAgents = []knownAgent{
	{"claude", []string{"claude"}, "type: claude\n    command: claude\n    models: [opus, sonnet, haiku]", "claude"},
	{"codex", []string{"codex", "npx"}, "type: acp\n    command: [npx, -y, \"@zed-industries/codex-acp\"]", "codex login"},
	{"gemini", []string{"gemini"}, "type: acp\n    command: [gemini, --acp]", "gemini"},
	{"kiro", []string{"kiro-cli"}, "type: acp\n    command: [kiro-cli, acp]", "kiro-cli login"},
	{"goose", []string{"goose"}, "type: acp\n    command: [goose, acp]", "goose configure"},
	{"opencode", []string{"opencode"}, "type: acp\n    command: [opencode, acp]", "opencode auth login"},
	{"copilot", []string{"copilot"}, "type: acp\n    command: [copilot, --acp, --stdio]", "copilot"},
	{"cursor", []string{"cursor-agent"}, "type: acp\n    command: [cursor-agent, acp]", "cursor-agent login"},
	{"aider", []string{"aider"}, "type: command\n    command: [aider, --message, \"{prompt}\", --yes-always, --no-pretty, --no-stream]\n    model_args: [--model, \"{model}\"]\n    image_args: [\"{path}\"]\n    timeout: 30m", "aider"},
}

func onPath(bins ...string) bool {
	for _, b := range bins {
		if _, err := exec.LookPath(b); err != nil {
			return false
		}
	}
	return true
}

type prompter struct{ r *bufio.Reader }

func (p prompter) ask(q, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", q, def)
	} else {
		fmt.Printf("%s: ", q)
	}
	line, _ := p.r.ReadString('\n')
	if line = strings.TrimSpace(line); line == "" {
		return def
	}
	return line
}

func (p prompter) yes(q string, def bool) bool {
	d := "y/N"
	if def {
		d = "Y/n"
	}
	a := strings.ToLower(p.ask(q+" ("+d+")", ""))
	if a == "" {
		return def
	}
	return strings.HasPrefix(a, "y")
}

const whisperModelURL = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small.bin"

func initConfig(cfgPath string, force bool) error {
	if _, err := os.Stat(cfgPath); err == nil && !force {
		return fmt.Errorf("%s already exists (use --force to overwrite, or edit it directly)", cfgPath)
	}
	home := filepath.Dir(cfgPath)
	p := prompter{bufio.NewReader(os.Stdin)}
	ctx := context.Background()

	fmt.Print("pocketagent setup\n\n")
	fmt.Println("1. Create a bot: message @BotFather on Telegram, send /newbot, and copy the token.")
	var token string
	var me tgUser
	for {
		token = p.ask("   Bot token", "")
		var err error
		if me, err = getMe(ctx, token); err == nil {
			break
		}
		fmt.Println("   That token didn't work:", err)
	}
	fmt.Printf("   ✓ Connected to @%s\n\n", me.Username)

	fmt.Printf("2. Open https://t.me/%s and send it any message, so I can learn your user id…\n", me.Username)
	wctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	user, err := waitForFirstMessage(wctx, token)
	cancel()
	if err != nil {
		return fmt.Errorf("no message arrived: %w", err)
	}
	name := user.FirstName
	if user.Username != "" {
		name += " (@" + user.Username + ")"
	}
	fmt.Printf("   ✓ Got a message from %s, id %d. Only this account will be allowed.\n\n", name, user.ID)

	fmt.Println("3. Agents found on this machine:")
	var found []knownAgent
	for _, a := range knownAgents {
		if onPath(a.bins...) {
			found = append(found, a)
			fmt.Println("   ✓", a.name)
		}
	}
	if len(found) == 0 {
		fmt.Println("   none. I'll add claude; install Claude Code (or edit the config for another agent).")
		found = []knownAgent{knownAgents[0]}
	}
	def := p.ask("   Default agent", found[0].name)

	userHome, _ := os.UserHomeDir()
	defCwd := filepath.Join(userHome, "projects")
	if _, err := os.Stat(defCwd); err != nil {
		defCwd = userHome
	}
	cwd := p.ask("\n4. Folder the agent starts in", strings.Replace(defCwd, userHome, "~", 1))

	fmt.Println("\n5. Voice messages")
	transcriber := "transcriber:\n  type: none   # whisper-cpp | openai | command"
	switch {
	case onPath("whisper-cli", "ffmpeg"):
		model := filepath.Join(home, "models", "ggml-small.bin")
		if _, err := os.Stat(model); err != nil {
			if p.yes("   whisper.cpp is installed. Download the multilingual small model (466 MB) for local transcription?", true) {
				if err := download(whisperModelURL, model); err != nil {
					fmt.Println("   download failed:", err)
				}
			}
		}
		if _, err := os.Stat(model); err == nil {
			transcriber = "transcriber:\n  type: whisper-cpp\n  model: " + model + "\n  language: auto"
			fmt.Println("   ✓ local whisper.cpp")
		}
	case os.Getenv("GROQ_API_KEY") != "":
		transcriber = "transcriber:\n  type: openai\n  base_url: https://api.groq.com/openai/v1\n  api_key: ${GROQ_API_KEY}\n  model: whisper-large-v3-turbo"
		fmt.Println("   ✓ Groq Whisper (GROQ_API_KEY is set)")
	case os.Getenv("OPENAI_API_KEY") != "":
		transcriber = "transcriber:\n  type: openai\n  api_key: ${OPENAI_API_KEY}\n  model: whisper-1"
		fmt.Println("   ✓ OpenAI Whisper (OPENAI_API_KEY is set)")
	default:
		fmt.Println("   off. For local voice: `brew install whisper-cpp ffmpeg` and run init again,")
		fmt.Println("   or set GROQ_API_KEY / OPENAI_API_KEY. See config.example.yaml.")
	}
	tts := "tts:\n  type: none   # say | openai | command"
	if runtime.GOOS == "darwin" && onPath("say", "ffmpeg") {
		tts = "tts:\n  type: say   # voice replies with /voice"
	}

	var agents strings.Builder
	for _, a := range found {
		fmt.Fprintf(&agents, "  %s:\n    %s\n", a.name, a.yaml)
	}
	body := fmt.Sprintf(`# pocketagent config. Every option is documented in config.example.yaml:
# https://github.com/c0sm0thecoder/pocketagent/blob/main/config.example.yaml

telegram:
  token: %q
  allowed_users: [%d]

defaults:
  agent: %s
  cwd: %s
  mode: ask            # ask | edits | plan | yolo

agents:
%s
%s

%s

budget:
  daily_usd: 0         # 0 = unlimited
`, token, user.ID, def, cwd, agents.String(), transcriber, tts)

	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		return err
	}
	if _, err := config.Load(cfgPath); err != nil {
		return fmt.Errorf("wrote %s but it doesn't load: %w", cfgPath, err)
	}
	fmt.Printf("\n✓ Wrote %s\n\nNext:\n  pocketagent start             run in the background\n  pocketagent service install   start at login and restart on crashes\n  pocketagent doctor            check everything\n", cfgPath)
	return nil
}

func download(url, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", resp.Status)
	}
	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	pw := &progressWriter{total: resp.ContentLength}
	_, err = io.Copy(f, io.TeeReader(resp.Body, pw))
	f.Close()
	fmt.Println()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

type progressWriter struct {
	total, n int64
	last     int
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.n += int64(len(b))
	if p.total > 0 {
		if pct := int(p.n * 100 / p.total); pct/5 != p.last/5 {
			p.last = pct
			fmt.Printf("\r   downloading… %d%%", pct)
		}
	}
	return len(b), nil
}

// ---------- doctor ----------

func doctor(cfgPath string) error {
	failed := false
	check := func(ok bool, what, hint string) {
		if ok {
			fmt.Println("✓", what)
			return
		}
		failed = true
		fmt.Println("✗", what)
		if hint != "" {
			fmt.Println("   →", hint)
		}
	}
	warn := func(what, hint string) {
		fmt.Println("!", what)
		if hint != "" {
			fmt.Println("   →", hint)
		}
	}

	cfg, err := config.Load(cfgPath)
	check(err == nil, "config "+cfgPath, errString(err)+" (run `pocketagent init`)")
	if err != nil {
		return fmt.Errorf("doctor found problems")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	me, err := getMe(ctx, cfg.Telegram.Token)
	check(err == nil, "telegram bot @"+me.Username, errString(err))
	check(len(cfg.Telegram.AllowedUsers) > 0, fmt.Sprintf("allowlist: %v", cfg.Telegram.AllowedUsers), "")

	for _, name := range cfg.AgentNames() {
		a := cfg.Agents[name]
		bin := ""
		if len(a.Wrap) > 0 {
			bin = a.Wrap[0]
		} else if len(a.Command) > 0 {
			bin = a.Command[0]
		} else {
			bin = "claude"
		}
		label := fmt.Sprintf("agent %s (%s: %s)", name, a.Type, bin)
		hint := bin + " is not on PATH"
		for _, k := range knownAgents {
			if k.name == name && k.login != "" {
				hint += "; after installing, log in once with `" + k.login + "`"
			}
		}
		check(onPath(bin), label, hint)
	}

	switch t := cfg.Transcriber; t.Type {
	case "none":
		warn("voice messages are off", "set transcriber in the config")
	case "whisper-cpp":
		bin := "whisper-cli"
		if len(t.Command) > 0 {
			bin = t.Command[0]
		}
		check(onPath(bin), "whisper.cpp ("+bin+")", "brew install whisper-cpp")
		_, err := os.Stat(t.Model)
		check(err == nil, "whisper model "+t.Model, "download one: "+whisperModelURL)
		check(onPath(t.FFmpeg), "ffmpeg", "brew install ffmpeg")
	case "openai":
		check(t.APIKey != "" || t.BaseURL != "", "transcription API "+t.BaseURL, "set transcriber.api_key")
	}
	switch cfg.TTS.Type {
	case "say":
		check(onPath("say", cfg.TTS.FFmpeg), "voice replies (say + ffmpeg)", "macOS only; needs ffmpeg")
	case "openai":
		check(cfg.TTS.APIKey != "" || cfg.TTS.BaseURL != "", "speech API", "set tts.api_key")
	}

	if cfg.CheckpointsEnabled() {
		check(onPath("git"), "git (for /diff and /undo)", "install git, or set checkpoints: false")
	}
	if fi, err := os.Stat(cfg.Defaults.Cwd); err != nil || !fi.IsDir() {
		check(false, "default cwd "+cfg.Defaults.Cwd, "create it or change defaults.cwd")
	}
	for name, p := range cfg.Projects {
		if fi, err := os.Stat(p.Cwd); err != nil || !fi.IsDir() {
			warn("project "+name+": "+p.Cwd+" does not exist", "")
		}
	}

	if pid := runningPID(cfg.Home); pid != 0 {
		fmt.Printf("✓ running (pid %d)\n", pid)
	} else {
		warn("not running", "pocketagent start")
	}
	if serviceInstalled() {
		fmt.Println("✓ service installed:", serviceFile())
	}
	if failed {
		return fmt.Errorf("doctor found problems")
	}
	return nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
