package main

import (
	"bufio"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/c0sm0thecoder/pocketagent/internal/config"
)

func TestRenderPlistIsValidXML(t *testing.T) {
	p := renderPlist(`/Apps/My Tools/pocketagent`, `/Users/a&b/.pocketagent/config.yaml`, `/opt/bin:/Users/x/My <bin>`, "/l.log")
	dec := xml.NewDecoder(strings.NewReader(p))
	dec.Strict = false // the DOCTYPE references an external DTD
	var strs []string
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("invalid XML: %v\n%s", err, p)
		}
		if cd, ok := tok.(xml.CharData); ok && strings.TrimSpace(string(cd)) != "" {
			strs = append(strs, string(cd))
		}
	}
	joined := strings.Join(strs, "|")
	for _, want := range []string{"/Apps/My Tools/pocketagent", "/Users/a&b/.pocketagent/config.yaml", "/opt/bin:/Users/x/My <bin>", "dev.pocketagent"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
}

func TestRenderUnitQuotes(t *testing.T) {
	u := renderUnit("/opt/my tools/pocketagent", "/home/a/cfg.yaml", `/usr/bin:/home/a/my bin`)
	for _, want := range []string{
		`ExecStart="/opt/my tools/pocketagent" run --config "/home/a/cfg.yaml"`,
		`Environment="PATH=/usr/bin:/home/a/my bin"`,
		"Restart=on-failure",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("missing %q in\n%s", want, u)
		}
	}
	if got := systemdQuote(`a"b\c%d`); got != `"a\"b\\c%%d"` {
		t.Errorf("quote = %s", got)
	}
}

func TestInstanceLock(t *testing.T) {
	home := t.TempDir()
	if pid := runningPID(home); pid != 0 {
		t.Fatalf("pid before lock = %d", pid)
	}
	lock, err := lockInstance(home)
	if err != nil {
		t.Fatal(err)
	}
	if pid := runningPID(home); pid != os.Getpid() {
		t.Errorf("running pid = %d, want %d", pid, os.Getpid())
	}
	if _, err := lockInstance(home); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("second lock: %v", err)
	}
	lock.Close()
	if pid := runningPID(home); pid != 0 {
		t.Errorf("pid after unlock = %d", pid)
	}
}

func TestLastLogLines(t *testing.T) {
	home := t.TempDir()
	if got := lastLogLines(home, 3); got != "" {
		t.Errorf("no log: %q", got)
	}
	os.WriteFile(filepath.Join(home, "pocketagent.log"), []byte("a\nb\nc\nd\n"), 0o600)
	if got := lastLogLines(home, 2); got != "c\nd" {
		t.Errorf("got %q", got)
	}
}

func TestPrompter(t *testing.T) {
	p := prompter{bufio.NewReader(strings.NewReader("\ncustom\n\nn\n"))}
	if got := p.ask("q", "default"); got != "default" {
		t.Errorf("empty answer = %q", got)
	}
	if got := p.ask("q", "default"); got != "custom" {
		t.Errorf("answer = %q", got)
	}
	if !p.yes("ok?", true) {
		t.Error("empty yes should take the default")
	}
	if p.yes("ok?", true) {
		t.Error("n should be no")
	}
}

func TestIndent(t *testing.T) {
	if got := indent("a\nb", "  "); got != "  a\n  b" {
		t.Errorf("got %q", got)
	}
}

func TestDoctorOnBrokenConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(p, []byte("telegram: {token: x}\n"), 0o600)
	if err := doctor(p); err == nil {
		t.Error("doctor passed a broken config")
	}
}

func TestParseSelection(t *testing.T) {
	cases := map[string]string{"": "", "all": "0,1,2,3", "1,3": "0,2", "2-4": "1,2,3", "1 1,2": "0,1"}
	for in, want := range cases {
		got, err := parseSelection(in, 4)
		var parts []string
		for _, i := range got {
			parts = append(parts, fmt.Sprint(i))
		}
		if err != nil || strings.Join(parts, ",") != want {
			t.Errorf("parseSelection(%q) = %v, %v; want %s", in, got, err, want)
		}
	}
	for _, bad := range []string{"0", "5", "x", "3-1", "1-9"} {
		if _, err := parseSelection(bad, 4); err == nil {
			t.Errorf("parseSelection(%q) accepted", bad)
		}
	}
}

// The projects section init writes is valid config, even for odd names.
func TestProjectsSetupOutputParses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "projects", "Odd: name #1")
	os.MkdirAll(dir, 0o700)
	p := prompter{bufio.NewReader(strings.NewReader("all\n"))}
	section := projectsSetup(p)
	cfg := "telegram: {token: x, allowed_users: [1]}\nagents: {a: {type: t, command: c}}\n" + section
	c, err := config.Parse([]byte(cfg), "/tmp/c.yaml")
	if err != nil {
		t.Fatalf("%v\n%s", err, cfg)
	}
	if c.Projects["Odd: name #1"].Cwd != dir {
		t.Errorf("projects = %+v", c.Projects)
	}
}
