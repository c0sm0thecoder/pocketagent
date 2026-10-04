package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/c0sm0thecoder/pocketagent/internal/config"
)

func TestAddProject(t *testing.T) {
	c, _ := setup(t, &fakeAgent{}, func(cfg *config.Config) {
		cfg.Path = "/etc/pocketagent.yaml"
		cfg.Projects = map[string]config.Project{"site": {Cwd: "/srv/site"}}
	})
	home := c.Settings(conv).Cwd
	api := filepath.Join(home, "api")
	os.Mkdir(api, 0o700)
	os.Mkdir(filepath.Join(home, "web"), 0o700)

	// Defaults: the conversation's folder, named after it.
	if _, err := c.SetCwd(conv, api); err != nil {
		t.Fatal(err)
	}
	p, err := c.AddProject(conv, "", "")
	if err != nil || p.Name != "api" || p.Cwd != api {
		t.Fatalf("add current folder: %+v %v", p, err)
	}
	// A relative path resolves against the conversation's folder.
	p, err = c.AddProject(conv, "frontend", "../web")
	if err != nil || p.Cwd != filepath.Join(home, "web") {
		t.Fatalf("add relative: %+v %v", p, err)
	}

	for _, tc := range []struct{ name, dir, want string }{
		{"api", filepath.Join(home), "already exists"},       // name taken (chat)
		{"site", filepath.Join(home), "already exists"},      // name taken (config)
		{"again", api, "already the project"},                // folder taken
		{"ghost", "/no/such/dir", "not a directory"},         // folder missing
		{"bad name!", home, "project names use"},             // invalid name
		{"-dash", home, "project names use"},                 // must start alphanumeric
		{strings.Repeat("x", 41), home, "project names use"}, // too long
	} {
		if _, err := c.AddProject(conv, tc.name, tc.dir); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("AddProject(%q, %q) = %v, want %q", tc.name, tc.dir, err, tc.want)
		}
	}

	var names []string
	for _, p := range c.Projects() {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "api,frontend,site" {
		t.Errorf("projects = %v", names)
	}
	if p, ok := c.ProjectAt(api); !ok || p.Name != "api" || p.FromConfig {
		t.Errorf("ProjectAt = %+v %v", p, ok)
	}

	// Switching to a chat-added project uses its folder.
	if err := c.SetProject(conv, "frontend"); err != nil {
		t.Fatal(err)
	}
	if s := c.Settings(conv); s.Cwd != filepath.Join(home, "web") || s.Project != "frontend" {
		t.Errorf("settings = %+v", s)
	}

	if err := c.RemoveProject("site"); err == nil || !strings.Contains(err.Error(), "/etc/pocketagent.yaml") {
		t.Errorf("removing a config project: %v", err)
	}
	if err := c.RemoveProject("api"); err != nil {
		t.Errorf("remove: %v", err)
	}
	if err := c.RemoveProject("api"); err == nil {
		t.Error("removed twice")
	}
	if _, ok := c.ProjectAt(api); ok {
		t.Error("removed project still listed")
	}
	if _, err := os.Stat(api); err != nil {
		t.Errorf("removing a project must not touch the folder: %v", err)
	}
}

// A config entry with the same name as a chat-added one wins.
func TestConfigProjectShadowsStored(t *testing.T) {
	c, _ := setup(t, &fakeAgent{}, nil)
	dir := t.TempDir()
	if _, err := c.AddProject(conv, "shared", dir); err != nil {
		t.Fatal(err)
	}
	c.Config.Projects = map[string]config.Project{"shared": {Cwd: "/from/config"}}
	ps := c.Projects()
	if len(ps) != 1 || ps[0].Cwd != "/from/config" || !ps[0].FromConfig {
		t.Errorf("projects = %+v", ps)
	}
}
