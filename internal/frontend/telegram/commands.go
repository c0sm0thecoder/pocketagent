package telegram

import (
	"context"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/core"
)

const helpText = `<b>pocketagent</b>: your coding agent in your pocket.

Send text, a voice note, or images (with an optional caption). Messages sent while the agent works are queued. When it wants to run a command or edit a file you get buttons; reply with text instead to deny and tell it what to do.

<b>Recommended: one topic per project</b>
1. Create a group with just you and me.
2. Group settings → Topics → on.
3. Make me an admin with <i>Manage Topics</i>.
4. Send /topics there. I'll create a topic for every project (and help you find them).

/agent · /model · /mode · /project: switch with one tap
/project add [name] [path] · /project remove &lt;name&gt;
/new: fresh session · /sessions: resume a recent one
/stop: cancel the run and the queue
/diff: what changed in the last turn (/diff all: since HEAD)
/undo: roll back the last turn's file changes
/cwd [path] · /voice · /status · /usage`

var modeHelp = map[agent.Mode]string{
	agent.ModeAsk:   "ask before anything that isn't read-only",
	agent.ModeEdits: "file edits allowed, commands still ask",
	agent.ModePlan:  "plan only, no changes",
	agent.ModeFull:  "allow everything (careful)",
}

func (b *Bot) handleCommand(ctx context.Context, conv core.ConvID, m *models.Message) {
	cmd, arg, _ := strings.Cut(strings.TrimSpace(m.Text), " ")
	cmd, _, _ = strings.Cut(cmd, "@") // "/new@MyBot" in groups
	arg = strings.TrimSpace(arg)
	c := b.core

	switch cmd {
	case "/start", "/help":
		b.sendHTML(ctx, conv, helpText, nil)

	case "/new":
		c.NewSession(conv)
		b.Notice(conv, "🆕 New session.")

	case "/stop":
		stopped, dropped := c.Stop(conv)
		switch {
		case !stopped:
			b.Notice(conv, "Nothing is running.")
		case dropped > 0:
			b.Notice(conv, fmt.Sprintf("Stopping… (dropped %d queued)", dropped))
		}

	case "/agent":
		if arg != "" {
			b.setAndReport(ctx, conv, nil, "agent", arg)
			return
		}
		b.picker(ctx, conv, nil, "agent")

	case "/model":
		if arg != "" {
			b.setAndReport(ctx, conv, nil, "model", arg)
			return
		}
		b.picker(ctx, conv, nil, "model")

	case "/mode":
		if arg != "" {
			b.setAndReport(ctx, conv, nil, "mode", arg)
			return
		}
		b.picker(ctx, conv, nil, "mode")

	case "/project":
		sub, rest, _ := strings.Cut(arg, " ")
		switch sub {
		case "":
			b.picker(ctx, conv, nil, "project")
		case "add":
			name, dir, _ := strings.Cut(strings.TrimSpace(rest), " ")
			b.addProject(ctx, conv, nil, name, strings.TrimSpace(dir))
		case "remove", "rm":
			if rest == "" {
				b.Notice(conv, "Usage: /project remove <name>")
				return
			}
			if err := c.RemoveProject(strings.TrimSpace(rest)); err != nil {
				b.Notice(conv, err.Error())
				return
			}
			b.sendHTML(ctx, conv, "🗑 Removed project <b>"+html.EscapeString(rest)+"</b>. The folder itself is untouched.", nil)
		default:
			b.setAndReport(ctx, conv, nil, "project", arg)
		}

	case "/topics":
		b.handleTopics(ctx, conv, m, arg)

	case "/sessions":
		b.picker(ctx, conv, nil, "session")

	case "/cwd":
		if arg == "" {
			b.sendHTML(ctx, conv, "📁 <code>"+html.EscapeString(c.Settings(conv).Cwd)+"</code>", nil)
			return
		}
		dir, err := c.SetCwd(conv, arg)
		if err != nil {
			b.Notice(conv, err.Error())
			return
		}
		b.sendHTML(ctx, conv, "📁 Now in <code>"+html.EscapeString(dir)+"</code> (new session)", nil)

	case "/diff":
		go b.sendDiff(conv, arg == "all") //nolint:gosec // outlives the update handler by design

	case "/undo":
		cp, err := c.Undo(ctx, conv)
		if err != nil {
			b.Notice(conv, "↩️ "+err.Error())
			return
		}
		b.sendHTML(ctx, conv, "↩️ Rolled back the changes from: <i>"+html.EscapeString(cp.Prompt)+"</i>\nThe agent's conversation still remembers that turn; tell it what you undid.", nil)

	case "/voice":
		on, err := c.ToggleVoice(conv)
		if err != nil {
			b.Notice(conv, err.Error())
			return
		}
		b.Notice(conv, map[bool]string{true: "🔊 Voice replies on.", false: "🔇 Voice replies off."}[on])

	case "/status":
		b.sendHTML(ctx, conv, b.statusText(conv), nil)

	case "/usage":
		today, month := c.Usage(userOf(m.From))
		text := fmt.Sprintf("💸 Today: $%.4f\nLast 30 days: $%.4f", today, month)
		if limit := b.cfg.Budget.DailyUSD; limit > 0 {
			text += fmt.Sprintf("\nDaily budget: $%.2f", limit)
		}
		text += "\n(only agents that report cost are counted)"
		b.Notice(conv, text)

	default:
		// Not ours: pass it to the agent (many agents have their own slash commands).
		c.Submit(conv, core.Input{User: userOf(m.From), Blocks: []agent.Block{{Text: m.Text}}, Text: m.Text})
	}
}

func (b *Bot) statusText(conv core.ConvID) string {
	s := b.core.Settings(conv)
	running, queued := b.core.Running(conv)
	session := s.SessionID
	if session == "" {
		session = "(new)"
	}
	model := s.Model
	if model == "" {
		model = "default"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>Status</b>\n")
	fmt.Fprintf(&sb, "Agent: <b>%s</b> · Model: %s · Mode: %s\n", html.EscapeString(s.Agent), html.EscapeString(model), s.Mode)
	if s.Project != "" {
		fmt.Fprintf(&sb, "Project: %s\n", html.EscapeString(s.Project))
	}
	fmt.Fprintf(&sb, "Cwd: <code>%s</code>\n", html.EscapeString(s.Cwd))
	fmt.Fprintf(&sb, "Session: <code>%s</code>\n", html.EscapeString(session))
	fmt.Fprintf(&sb, "Running: %v", running)
	if queued > 0 {
		fmt.Fprintf(&sb, " (%d queued)", queued)
	}
	if len(s.AlwaysAllow) > 0 {
		fmt.Fprintf(&sb, "\nAlways allowed: %s", html.EscapeString(strings.Join(s.AlwaysAllow, ", ")))
	}
	fmt.Fprintf(&sb, "\nVoice replies: %v\nSpent in this conversation: $%.4f", s.Voice, s.TotalCost)
	return sb.String()
}

func (b *Bot) sendDiff(conv core.ConvID, all bool) {
	ctx := context.Background()
	d, err := b.core.Diff(ctx, conv, all)
	switch {
	case err != nil:
		b.Notice(conv, "Δ "+err.Error())
	case strings.TrimSpace(d) == "":
		b.Notice(conv, "Δ No changes.")
	case len(d) < 3500:
		b.sendHTML(ctx, conv, `<pre><code class="language-diff">`+html.EscapeString(d)+"</code></pre>", nil)
	default:
		stat, _, _ := strings.Cut(d, "\ndiff --git")
		if len(stat) > 1500 {
			stat = stat[:1500] + "\n…"
		}
		b.sendHTML(ctx, conv, "<pre>"+html.EscapeString(stat)+"</pre>", nil)
		logErr("send diff", b.sendDocument(ctx, conv, "changes.diff", []byte(d), "Full diff"))
	}
}

// ---------- pickers ----------

type choice struct{ label, value string }

func (b *Bot) choices(conv core.ConvID, kind string) (title string, list []choice, current string) {
	c := b.core
	s := c.Settings(conv)
	switch kind {
	case "agent":
		for _, n := range c.AgentNames() {
			list = append(list, choice{n + " (" + c.AgentType(n) + ")", n})
		}
		return "Choose an agent", list, s.Agent
	case "model":
		list = append(list, choice{"default", ""})
		for _, m := range c.Models(conv) {
			list = append(list, choice{m, m})
		}
		return "Model for " + s.Agent + " (or /model <name>)", list, s.Model
	case "mode":
		caps := c.Caps(conv)
		for _, m := range agent.Modes {
			if caps.SupportsMode(m) {
				list = append(list, choice{string(m) + ": " + modeHelp[m], string(m)})
			}
		}
		return "Permission mode", list, string(s.Mode)
	case "project":
		for _, p := range c.Projects() {
			list = append(list, choice{p.Name + " · " + shortPath(p.Cwd), p.Name})
		}
		return "Choose a project", list, s.Project
	case "session":
		for _, r := range c.Sessions(conv) {
			label := r.Updated.Format("Jan 2 15:04") + " · " + r.Agent + " · " + r.Title
			list = append(list, choice{label, r.ID})
		}
		return "Resume a session", list, s.SessionID
	}
	return "", nil, ""
}

// picker sends (or, when msg is set, edits) an inline keyboard. Buttons carry
// the list index; the list is rebuilt on tap.
func (b *Bot) picker(ctx context.Context, conv core.ConvID, msg *models.Message, kind string) {
	title, list, current := b.choices(conv, kind)
	var rows, actions [][]models.InlineKeyboardButton
	if kind == "project" {
		// Projects can be added right from the picker: the folder the
		// conversation is in (unless it already is one), or suggestions.
		cwd := b.core.Settings(conv).Cwd
		if _, ok := b.core.ProjectAt(cwd); !ok {
			actions = append(actions, []models.InlineKeyboardButton{{
				Text: "➕ Add this folder (" + filepath.Base(cwd) + ")", CallbackData: "pa|add|0",
			}})
		}
		actions = append(actions, []models.InlineKeyboardButton{{Text: "🔎 Find projects", CallbackData: "sg|show|p"}})
	}
	if len(list) == 0 && len(actions) == 0 {
		b.Notice(conv, "Nothing to choose from yet.")
		return
	}
	for i, ch := range list {
		label := ch.label
		if ch.value == current {
			label = "✓ " + label
		}
		if len(label) > 60 {
			label = label[:57] + "..."
		}
		rows = append(rows, []models.InlineKeyboardButton{{Text: label, CallbackData: fmt.Sprintf("pk|%s|%d", kind, i)}})
	}
	rows = append(rows, actions...)
	b.editHTML(ctx, conv, msg, "<b>"+html.EscapeString(title)+"</b>", &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// addProject registers an existing folder as a project and reports it,
// editing msg when the request came from a button.
func (b *Bot) addProject(ctx context.Context, conv core.ConvID, msg *models.Message, name, dir string) {
	p, err := b.core.AddProject(conv, name, dir)
	if err != nil {
		b.editHTML(ctx, conv, msg, html.EscapeString(err.Error()), nil)
		return
	}
	b.editHTML(ctx, conv, msg, fmt.Sprintf("➕ Added project <b>%s</b> (<code>%s</code>).\nSwitch to it anytime with /project.",
		html.EscapeString(p.Name), html.EscapeString(shortPath(p.Cwd))), nil)
}

// shortPath shows paths under the home directory as ~/...
func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}

func (b *Bot) setAndReport(ctx context.Context, conv core.ConvID, msg *models.Message, kind, value string) {
	c := b.core
	var err error
	var text string
	switch kind {
	case "agent":
		err = c.SetAgent(conv, value)
		text = "🤖 Agent: <b>" + html.EscapeString(value) + "</b> (new session)"
	case "model":
		c.SetModel(conv, value)
		if value == "" {
			value = "default"
		}
		text = "🧠 Model: <b>" + html.EscapeString(value) + "</b>"
	case "mode":
		err = c.SetMode(conv, agent.Mode(value))
		text = "🛡 Mode: <b>" + html.EscapeString(value) + "</b>: " + modeHelp[agent.Mode(value)]
	case "project":
		err = c.SetProject(conv, value)
		if err == nil {
			s := c.Settings(conv)
			text = fmt.Sprintf("📂 Project <b>%s</b>: %s in <code>%s</code> (new session)",
				html.EscapeString(value), html.EscapeString(s.Agent), html.EscapeString(s.Cwd))
		}
	case "session":
		r, e := c.ResumeSession(conv, value)
		err = e
		text = "↪️ Resumed: <i>" + html.EscapeString(r.Title) + "</i> (" + html.EscapeString(r.Agent) + ")"
	}
	if err != nil {
		text = html.EscapeString(err.Error())
	}
	b.editHTML(ctx, conv, msg, text, nil)
}

func (b *Bot) handleCallback(ctx context.Context, q *models.CallbackQuery) {
	answer := func(text string) {
		_, err := b.tg.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: text})
		logErr("answer callback", err)
	}
	if !b.allowed(q.From.ID) {
		answer("Not authorized")
		return
	}
	parts := strings.Split(q.Data, "|")
	if len(parts) != 3 {
		answer("")
		return
	}
	idx, _ := strconv.Atoi(parts[2])

	switch parts[0] {
	case "ap":
		b.mu.Lock()
		opts := b.approvals[parts[1]]
		b.mu.Unlock()
		if idx < 0 || idx >= len(opts) || !b.core.Answer(parts[1], opts[idx].ID) {
			answer("This request has expired.")
			return
		}
		answer("")

	case "sg":
		if q.Message.Message == nil {
			answer("")
			return
		}
		answer("")
		if parts[1] == "show" {
			msg := q.Message.Message
			b.showSuggestions(ctx, convOf(msg), msg, parts[2] == "t", "")
			return
		}
		b.pickSuggestion(ctx, q, parts[1], parts[2])

	case "pa":
		msg := q.Message.Message
		if msg == nil {
			answer("")
			return
		}
		answer("")
		b.addProject(ctx, convOf(msg), msg, "", "")

	case "pk":
		msg := q.Message.Message
		if msg == nil {
			answer("")
			return
		}
		conv := convOf(msg)
		_, list, _ := b.choices(conv, parts[1])
		if idx < 0 || idx >= len(list) {
			answer("That list changed; try again.")
			return
		}
		answer("")
		b.setAndReport(ctx, conv, msg, parts[1], list[idx].value)
	}
}
