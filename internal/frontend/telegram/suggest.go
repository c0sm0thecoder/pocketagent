package telegram

import (
	"context"
	"fmt"
	"html"
	"strings"

	"github.com/go-telegram/bot/models"

	"github.com/c0sm0thecoder/pocketagent/internal/core"
)

// suggestLimit is how many folders are offered at once.
const suggestLimit = 10

// Buttons carry "sg|<index>|<then>": index into the current suggestions (or
// "all"), and then "t" to create topics afterwards or "p" to stop.

// showSuggestions offers folders to add as projects, editing msg if set.
func (b *Bot) showSuggestions(ctx context.Context, conv core.ConvID, msg *models.Message, topics bool, note string) {
	cands := b.core.SuggestProjects(suggestLimit)
	then := "p"
	if topics {
		then = "t"
	}
	var rows [][]models.InlineKeyboardButton
	for i, c := range cands {
		label := "➕ " + c.Name + " · " + shortPath(c.Path)
		if len(label) > 60 {
			label = label[:57] + "..."
		}
		rows = append(rows, []models.InlineKeyboardButton{{Text: label, CallbackData: fmt.Sprintf("sg|%d|%s", i, then)}})
	}
	text := note
	switch {
	case len(cands) == 0 && topics:
		text += "I couldn't find candidate folders. Add projects with /project add &lt;name&gt; &lt;path&gt;, then send /topics again."
	case len(cands) == 0:
		text += "I couldn't find candidate folders. Add one with /project add &lt;name&gt; &lt;path&gt;."
	default:
		text += "<b>Pick folders to add as projects</b>\nFound in your code folders and recent agent sessions. Tap to add one at a time."
		all := "✅ Add all"
		if topics {
			all = "✅ Add all and create topics"
			text += " When you're done, send /topics."
		}
		rows = append(rows, []models.InlineKeyboardButton{{Text: all, CallbackData: "sg|all|" + then}})
	}
	var markup models.ReplyMarkup
	if len(rows) > 0 {
		markup = &models.InlineKeyboardMarkup{InlineKeyboard: rows}
	}
	b.editHTML(ctx, conv, msg, strings.TrimSpace(text), markup)
}

// pickSuggestion handles a tap on a suggestion button.
func (b *Bot) pickSuggestion(ctx context.Context, q *models.CallbackQuery, which, then string) {
	msg := q.Message.Message
	conv := convOf(msg)
	cands := b.core.SuggestProjects(suggestLimit)
	var picked []string
	add := func(name, path string) {
		if p, err := b.core.AddProject(conv, name, path); err == nil {
			picked = append(picked, p.Name)
		}
	}
	if which == "all" {
		for _, c := range cands {
			add(c.Name, c.Path)
		}
	} else {
		var i int
		if _, err := fmt.Sscan(which, &i); err != nil || i < 0 || i >= len(cands) {
			b.showSuggestions(ctx, conv, msg, then == "t", "That list changed. ")
			return
		}
		add(cands[i].Name, cands[i].Path)
	}

	note := ""
	if len(picked) > 0 {
		note = "➕ Added " + html.EscapeString(strings.Join(picked, ", ")) + ".\n\n"
	}
	if which == "all" && then == "t" {
		b.editHTML(ctx, conv, msg, strings.TrimSpace(note)+"\nCreating topics…", nil)
		b.handleTopics(ctx, conv, msg, "")
		return
	}
	b.showSuggestions(ctx, conv, msg, then == "t", note)
}
