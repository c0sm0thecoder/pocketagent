package core

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/c0sm0thecoder/pocketagent/internal/config"
	"github.com/c0sm0thecoder/pocketagent/internal/store"
)

// ProjectInfo describes a project for listing.
type ProjectInfo struct {
	Name       string
	Cwd        string
	FromConfig bool // defined in the config file, so it can't be removed from chat
}

var projectName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$`)

// project looks a project up by name. Config entries win over projects
// added from chat, so the file stays the source of truth.
func (c *Core) project(name string) (config.Project, bool) {
	if p, ok := c.Config.Projects[name]; ok {
		return p, true
	}
	if p, ok := c.Store.Projects()[name]; ok {
		return config.Project{Cwd: p.Cwd}, true
	}
	return config.Project{}, false
}

// Projects lists every project, from the config file and from chat, by name.
func (c *Core) Projects() []ProjectInfo {
	var out []ProjectInfo
	for name, p := range c.Config.Projects {
		out = append(out, ProjectInfo{Name: name, Cwd: p.Cwd, FromConfig: true})
	}
	for name, p := range c.Store.Projects() {
		if _, shadowed := c.Config.Projects[name]; !shadowed {
			out = append(out, ProjectInfo{Name: name, Cwd: p.Cwd})
		}
	}
	slices.SortFunc(out, func(a, b ProjectInfo) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		}
		return 0
	})
	return out
}

// ProjectAt returns the project whose folder is dir, if any.
func (c *Core) ProjectAt(dir string) (ProjectInfo, bool) {
	for _, p := range c.Projects() {
		if p.Cwd == dir {
			return p, true
		}
	}
	return ProjectInfo{}, false
}

// AddProject registers an existing folder as a project. dir defaults to the
// conversation's working directory and name to the folder's name.
func (c *Core) AddProject(conv ConvID, name, dir string) (ProjectInfo, error) {
	if dir == "" {
		dir = c.Settings(conv).Cwd
	}
	dir, err := c.resolveDir(conv, dir)
	if err != nil {
		return ProjectInfo{}, err
	}
	if name == "" {
		name = filepath.Base(dir)
	}
	if !projectName.MatchString(name) {
		return ProjectInfo{}, fmt.Errorf("project names use letters, digits, '.', '_' and '-' (up to 40), and start with a letter or digit: %q", name)
	}
	if _, ok := c.project(name); ok {
		return ProjectInfo{}, fmt.Errorf("a project named %q already exists", name)
	}
	if p, ok := c.ProjectAt(dir); ok {
		return ProjectInfo{}, fmt.Errorf("%s is already the project %q", dir, p.Name)
	}
	if err := c.Store.AddProject(name, store.Project{Cwd: dir, Added: time.Now()}); err != nil {
		return ProjectInfo{}, err
	}
	return ProjectInfo{Name: name, Cwd: dir}, nil
}

// RemoveProject forgets a project added from chat. The folder is untouched.
func (c *Core) RemoveProject(name string) error {
	if _, ok := c.Config.Projects[name]; ok {
		return fmt.Errorf("%q is defined in %s; remove it there", name, c.Config.Path)
	}
	return c.Store.RemoveProject(name)
}

// BoundConversations maps each conversation that has a project to it.
func (c *Core) BoundConversations() map[ConvID]string {
	out := map[ConvID]string{}
	for key, p := range c.Store.ProjectOf() {
		out[ConvID(key)] = p
	}
	return out
}
