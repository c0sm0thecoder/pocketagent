package telegram

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
)

// fakeHome makes a home directory with two projects in ~/projects, the
// first more recently used.
func fakeHome(t *testing.T) (home, api, web string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	api = filepath.Join(home, "projects", "api")
	web = filepath.Join(home, "projects", "web")
	for _, d := range []string{api, web} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(web, old, old)
	return home, api, web
}

func TestTopicsWithoutProjectsOffersSuggestions(t *testing.T) {
	_, api_, web := fakeHome(t)
	b, api, _ := setup(t)
	ctx := context.Background()

	b.handleUpdate(ctx, nil, forumMessage("/topics"))
	c := api.find(t, "sendMessage", "No projects yet")
	rows := buttons(t, c)
	button(t, rows, "➕ api · ~/projects/api")
	button(t, rows, "➕ web · ")

	// Add one: the message updates and the added folder leaves the list.
	b.handleUpdate(ctx, nil, forumCallback(button(t, rows, "➕ api").CallbackData))
	edit := api.find(t, "editMessageText", "Added api")
	rows = buttons(t, edit)
	for _, r := range rows {
		if r[0].Text == "➕ api · ~/projects/api" {
			t.Fatal("added folder still suggested")
		}
	}
	if p, ok := b.core.ProjectAt(api_); !ok || p.Name != "api" {
		t.Errorf("api not added: %+v", p)
	}

	// Add the rest and create the topics in one tap.
	b.handleUpdate(ctx, nil, forumCallback(button(t, rows, "✅ Add all and create topics").CallbackData))
	api.find(t, "editMessageText", "Added web")
	if _, ok := b.core.ProjectAt(web); !ok {
		t.Error("web not added")
	}
	for range 2 {
		api.wait(t, "createForumTopic")
	}
	api.find(t, "sendMessage", "Created topics: <b>api, web</b>")
}

func TestFindProjectsFromPicker(t *testing.T) {
	_, _, web := fakeHome(t)
	b, api, _ := setup(t)
	ctx := context.Background()
	send(b, "/project")
	rows := buttons(t, api.find(t, "sendMessage", "Choose a project"))
	b.handleUpdate(ctx, nil, callback(button(t, rows, "🔎 Find projects").CallbackData, 0))
	rows = buttons(t, api.find(t, "editMessageText", "Pick folders to add as projects"))
	b.handleUpdate(ctx, nil, callback(button(t, rows, "✅ Add all").CallbackData, 0))
	api.find(t, "editMessageText", "Added api, web")
	if _, ok := b.core.ProjectAt(web); !ok {
		t.Error("web not added")
	}
}

func TestNoSuggestions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	b, api, _ := setup(t)
	b.handleUpdate(context.Background(), nil, forumMessage("/topics"))
	api.find(t, "sendMessage", "couldn't find candidate folders")
}

func forumCallback(data string) *models.Update {
	u := callback(data, 0)
	u.CallbackQuery.Message.Message.Chat.IsForum = true
	return u
}
