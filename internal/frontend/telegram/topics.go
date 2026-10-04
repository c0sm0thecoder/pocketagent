package telegram

import (
	"context"
	"fmt"
	"html"
	"slices"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/c0sm0thecoder/pocketagent/internal/core"
)

// topicColors are the icon colors Telegram allows for forum topics.
var topicColors = []int{0x6FB9F0, 0xFFD67E, 0xCB86DB, 0x8EEE98, 0xFF93B2, 0xFB6F5F}

// handleTopics creates a forum topic for each project that doesn't have one
// in this group yet (or for the named projects) and binds it to the project.
func (b *Bot) handleTopics(ctx context.Context, conv core.ConvID, m *models.Message, arg string) {
	if !m.Chat.IsForum {
		b.sendHTML(ctx, conv, "<b>/topics</b> works in a group with Topics turned on.\n\n"+
			"1. Create a group and add me.\n2. Group settings → Topics → on.\n"+
			"3. Make me an admin with <i>Manage Topics</i>.\n4. Send /topics in the group.", nil)
		return
	}
	projects := b.core.Projects()
	if len(projects) == 0 {
		b.Notice(conv, "No projects yet. Add one with /project add, or under projects: in "+b.cfg.Path)
		return
	}

	wanted := strings.Fields(arg)
	have := map[string]bool{} // projects that already have a topic in this chat
	prefix := fmt.Sprintf("%d:", m.Chat.ID)
	for id, project := range b.core.BoundConversations() {
		if strings.HasPrefix(string(id), prefix) && !strings.HasSuffix(string(id), ":0") {
			have[project] = true
		}
	}

	var created, skipped, failed []string
	for i, p := range projects {
		switch {
		case len(wanted) > 0 && !slices.Contains(wanted, p.Name):
			continue
		case len(wanted) == 0 && have[p.Name]:
			skipped = append(skipped, p.Name)
			continue
		}
		topic, err := b.tg.CreateForumTopic(ctx, &bot.CreateForumTopicParams{
			ChatID: m.Chat.ID, Name: p.Name, IconColor: topicColors[i%len(topicColors)],
		})
		if err != nil {
			failed = append(failed, p.Name+": "+err.Error())
			if strings.Contains(err.Error(), "not enough rights") || strings.Contains(err.Error(), "CHAT_ADMIN_REQUIRED") {
				break
			}
			continue
		}
		topicConv := core.ConvID(fmt.Sprintf("%d:%d", m.Chat.ID, topic.MessageThreadID))
		if err := b.core.SetProject(topicConv, p.Name); err != nil {
			failed = append(failed, p.Name+": "+err.Error())
			continue
		}
		s := b.core.Settings(topicConv)
		b.sendHTML(ctx, topicConv, fmt.Sprintf("📂 <b>%s</b> · %s · %s mode\n<code>%s</code>\n\nSend a message, a voice note or a screenshot to start.",
			html.EscapeString(p.Name), html.EscapeString(s.Agent), s.Mode, html.EscapeString(shortPath(s.Cwd))), nil)
		created = append(created, p.Name)
	}
	for _, n := range wanted {
		if !slices.ContainsFunc(projects, func(p core.ProjectInfo) bool { return p.Name == n }) {
			failed = append(failed, n+": no such project")
		}
	}

	var sb strings.Builder
	if len(created) > 0 {
		fmt.Fprintf(&sb, "🗂 Created topics: <b>%s</b>\n", html.EscapeString(strings.Join(created, ", ")))
	}
	if len(skipped) > 0 {
		fmt.Fprintf(&sb, "Already have topics: %s (recreate with /topics &lt;name&gt;)\n", html.EscapeString(strings.Join(skipped, ", ")))
	}
	if len(failed) > 0 {
		fmt.Fprintf(&sb, "⚠️ %s\n", html.EscapeString(strings.Join(failed, "\n")))
		if strings.Contains(strings.Join(failed, " "), "rights") || strings.Contains(strings.Join(failed, " "), "ADMIN") {
			sb.WriteString("Make me an admin with <i>Manage Topics</i>, then send /topics again.\n")
		}
	}
	if sb.Len() == 0 {
		sb.WriteString("Nothing to do.")
	}
	b.sendHTML(ctx, conv, strings.TrimSpace(sb.String()), nil)
}
