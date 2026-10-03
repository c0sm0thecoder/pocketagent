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
	"github.com/c0sm0thecoder/pocketagent/internal/registry"
)

const whisperModelURL = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small.bin"

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
	hint := map[bool]string{true: "Y/n", false: "y/N"}[def]
	a := strings.ToLower(p.ask(q+" ("+hint+")", ""))
	if a == "" {
		return def
	}
	return strings.HasPrefix(a, "y")
}

// indent prefixes every line of s.
func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}

func initConfig(cfgPath string, force bool) error {
	if _, err := os.Stat(cfgPath); err == nil && !force {
		return fmt.Errorf("%s already exists (use --force to overwrite, or edit it directly)", cfgPath)
	}
	home := filepath.Dir(cfgPath)
	p := prompter{bufio.NewReader(os.Stdin)}
	ctx := context.Background()

	fmt.Print("pocketagent setup\n\n")
	fmt.Println("1. Create a Telegram bot: message @BotFather, send /newbot, and copy the token.")
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
	fmt.Printf("   ✓ Message from %s (id %d). Only this account will be allowed.\n\n", user.display(), user.ID)

	fmt.Println("3. Agents found on this machine:")
	var found []registry.Preset
	for _, pr := range registry.Presets {
		if onPath(pr.Requires...) {
			found = append(found, pr)
			fmt.Println("   ✓", pr.Name)
		}
	}
	if len(found) == 0 {
		var names []string
		for _, pr := range registry.Presets {
			names = append(names, pr.Name)
		}
		return fmt.Errorf("no known agent is installed; install one (%s) or write the agents section by hand from config.example.yaml", strings.Join(names, ", "))
	}
	def := found[0].Name
	if len(found) > 1 {
		def = p.ask("   Default agent", def)
	}

	userHome, _ := os.UserHomeDir()
	defCwd := filepath.Join(userHome, "projects")
	if _, err := os.Stat(defCwd); err != nil {
		defCwd = userHome
	}
	cwd := p.ask("\n4. Folder the agent starts in", strings.Replace(defCwd, userHome, "~", 1))

	fmt.Println("\n5. Voice messages")
	transcriber := voiceSetup(p, home)
	tts := "tts:\n  type: none           # say | http | command"
	if runtime.GOOS == "darwin" && onPath("say", "ffmpeg") {
		tts = "tts:\n  type: say            # spoken replies with /voice"
	}

	var agents strings.Builder
	for _, pr := range found {
		fmt.Fprintf(&agents, "  %s:\n%s\n", pr.Name, indent(pr.Config, "    "))
	}
	body := fmt.Sprintf(`# pocketagent config. Every option is documented in config.example.yaml.

telegram:
  token: %q
  allowed_users: [%d]

defaults:
  agent: %s
  cwd: %s
  mode: ask            # ask | edits | plan | full

agents:
%s
%s

%s
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

// voiceSetup offers local transcription when whisper.cpp is installed, and
// otherwise any HTTP transcription endpoint.
func voiceSetup(p prompter, home string) string {
	off := "transcriber:\n  type: none           # whisper-cpp | http | command"
	if onPath("whisper-cli", "ffmpeg") {
		model := filepath.Join(home, "models", "ggml-small.bin")
		if _, err := os.Stat(model); err != nil && p.yes("   whisper.cpp is installed. Download the multilingual small model (466 MB)?", true) {
			if err := download(whisperModelURL, model); err != nil {
				fmt.Println("   download failed:", err)
			}
		}
		if _, err := os.Stat(model); err == nil {
			fmt.Println("   ✓ local transcription with whisper.cpp")
			return "transcriber:\n  type: whisper-cpp\n  model: " + model + "\n  language: auto"
		}
	} else {
		fmt.Println("   For local transcription install whisper.cpp and ffmpeg, then run init again.")
	}
	base := p.ask("   Or a transcription API base URL (OpenAI-compatible /audio/transcriptions; blank to skip)", "")
	if base == "" {
		fmt.Println("   voice messages off")
		return off
	}
	model := p.ask("   Model name", "")
	keyVar := p.ask("   Environment variable holding the API key (blank if none)", "")
	cfg := "transcriber:\n  type: http\n  base_url: " + base + "\n  model: " + model
	if keyVar != "" {
		cfg += "\n  api_key: ${" + keyVar + "}"
	}
	return cfg
}

func download(url, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
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
	_, err = io.Copy(f, io.TeeReader(resp.Body, &progressWriter{total: resp.ContentLength}))
	// A failed Close can mean the data never reached the disk.
	if cerr := f.Close(); err == nil {
		err = cerr
	}
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
