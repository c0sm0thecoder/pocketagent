package telegram

import (
	"context"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/config"
)

func forumMessage(text string) *models.Update {
	u := message(owner, 0, text)
	u.Message.Chat.IsForum = true
	return u
}

func TestTopicsCreatesOnePerProject(t *testing.T) {
	b, api, agents := setup(t)
	api_, web := t.TempDir(), t.TempDir()
	b.cfg.Projects = map[string]config.Project{"api": {Cwd: api_}, "web": {Cwd: web}}
	ctx := context.Background()

	b.handleUpdate(ctx, nil, forumMessage("/topics"))
	var threads []string
	for _, name := range []string{"api", "web"} {
		c := api.wait(t, "createForumTopic")
		if c.params["name"] != name {
			t.Fatalf("topic name = %q, want %q", c.params["name"], name)
		}
		welcome := api.find(t, "sendMessage", "📂 <b>"+name+"</b>")
		threads = append(threads, welcome.params["message_thread_id"])
	}
	api.find(t, "sendMessage", "Created topics: <b>api, web</b>")
	if threads[0] == "" || threads[0] == "0" || threads[0] == threads[1] {
		t.Fatalf("welcome messages not in their own topics: %v", threads)
	}

	// The new topic is bound: messages there run in the project's folder.
	got := make(chan string, 1)
	agents["alpha"].run = func(_ context.Context, req agent.Request, h agent.Handler) (agent.Result, error) {
		got <- req.Cwd
		return agent.Result{}, nil
	}
	in := forumMessage("hello api")
	in.Message.MessageThreadID, in.Message.IsTopicMessage = atoi(threads[0]), true
	b.handleUpdate(ctx, nil, in)
	if cwd := <-got; cwd != api_ {
		t.Errorf("topic runs in %q, want %q", cwd, api_)
	}

	// Running it again creates nothing new.
	b.handleUpdate(ctx, nil, forumMessage("/topics"))
	api.find(t, "sendMessage", "Already have topics: api, web")

	// A named project is (re)created on request.
	b.handleUpdate(ctx, nil, forumMessage("/topics web nope"))
	if c := api.wait(t, "createForumTopic"); c.params["name"] != "web" {
		t.Errorf("recreated %q", c.params["name"])
	}
	api.find(t, "sendMessage", "nope: no such project")
}

func TestTopicsNeedsAForum(t *testing.T) {
	b, api, _ := setup(t)
	send(b, "/topics")
	api.find(t, "sendMessage", "works in a group with Topics turned on")
}

func TestTopicsWithoutProjects(t *testing.T) {
	b, api, _ := setup(t)
	b.handleUpdate(context.Background(), nil, forumMessage("/topics"))
	api.find(t, "sendMessage", "No projects yet")
}

func atoi(s string) int {
	n := 0
	for _, r := range strings.TrimSpace(s) {
		n = n*10 + int(r-'0')
	}
	return n
}

// Telegram posts "topic created" (and pin) notices as the bot itself. They
// must not get a reply, and neither must other bots or strangers in groups.
func TestIgnoresServiceMessagesBotsAndGroupStrangers(t *testing.T) {
	b, api, _ := setup(t)
	ctx := context.Background()

	service := forumMessage("")
	service.Message.From = &models.User{ID: 999, IsBot: true, Username: "pocketagent_bot"}
	service.Message.ForumTopicCreated = &models.ForumTopicCreated{Name: "api"}
	b.handleUpdate(ctx, nil, service)

	otherBot := forumMessage("hello")
	otherBot.Message.From = &models.User{ID: 998, IsBot: true}
	b.handleUpdate(ctx, nil, otherBot)

	stranger := forumMessage("rm -rf please")
	stranger.Message.From = &models.User{ID: 666}
	b.handleUpdate(ctx, nil, stranger)

	// A private message from a stranger still gets the setup hint.
	private := message(666, 0, "hi")
	private.Message.Chat.Type = models.ChatTypePrivate
	b.handleUpdate(ctx, nil, private)
	c := api.wait(t, "sendMessage")
	if !strings.Contains(c.params["text"], "Not authorized. Your Telegram user id is 666") {
		t.Fatalf("first reply was %q: something in the group got a reply", c.params["text"])
	}
}

func TestAnonymousAdminGetsAHint(t *testing.T) {
	b, api, agents := setup(t)
	ran := make(chan bool, 1)
	agents["alpha"].run = func(context.Context, agent.Request, agent.Handler) (agent.Result, error) {
		ran <- true
		return agent.Result{}, nil
	}
	anon := forumMessage("deploy it")
	anon.Message.From = &models.User{ID: 1087968824, IsBot: true, Username: "GroupAnonymousBot"}
	anon.Message.SenderChat = &models.Chat{ID: anon.Message.Chat.ID}
	b.handleUpdate(context.Background(), nil, anon)
	api.find(t, "sendMessage", "posting anonymously")
	select {
	case <-ran:
		t.Fatal("an anonymous message ran the agent")
	default:
	}
}
