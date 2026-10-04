// Package telegram is the Telegram frontend: it turns updates into core
// calls and implements core.UI.
//
// Conversations are identified as "<chat id>:<thread id>", so each forum
// topic is its own conversation. Users are identified by their numeric id.
package telegram

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/config"
	"github.com/c0sm0thecoder/pocketagent/internal/core"
)

type Bot struct {
	tg   *bot.Bot
	core *core.Core
	cfg  *config.Config

	mu        sync.Mutex
	albums    map[string]*album
	approvals map[string][]agent.Option // approval id -> options, for button callbacks
}

type album struct {
	conv   core.ConvID
	user   string
	blocks []agent.Block
	text   []string
}

// New creates the bot. Extra options are passed to the Telegram client
// (tests use them to point it at a fake API server).
func New(cfg *config.Config, c *core.Core, opts ...bot.Option) (*Bot, error) {
	b := &Bot{core: c, cfg: cfg, albums: map[string]*album{}, approvals: map[string][]agent.Option{}}
	tg, err := bot.New(cfg.Telegram.Token, append([]bot.Option{bot.WithDefaultHandler(b.handleUpdate)}, opts...)...)
	if err != nil {
		return nil, err
	}
	b.tg = tg
	c.SetUI(b)
	return b, nil
}

var commands = []models.BotCommand{
	{Command: "new", Description: "Start a fresh session"},
	{Command: "stop", Description: "Cancel the current run and the queue"},
	{Command: "agent", Description: "Switch coding agent"},
	{Command: "model", Description: "Switch model"},
	{Command: "mode", Description: "Permission mode: ask, edits, plan, full"},
	{Command: "project", Description: "Switch project (/project add to register a folder)"},
	{Command: "cwd", Description: "Show or change working directory"},
	{Command: "topics", Description: "Create a forum topic per project (groups)"},
	{Command: "sessions", Description: "Resume a recent session"},
	{Command: "diff", Description: "Changes since the last message (/diff all: since HEAD)"},
	{Command: "undo", Description: "Roll back the last turn's file changes"},
	{Command: "voice", Description: "Toggle voice replies"},
	{Command: "status", Description: "Current settings"},
	{Command: "usage", Description: "Spend today and this month"},
	{Command: "help", Description: "How to use this bot"},
}

// Run blocks until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) {
	if _, err := b.tg.SetMyCommands(ctx, &bot.SetMyCommandsParams{Commands: commands}); err != nil {
		log.Printf("set commands: %v", err)
	}
	b.tg.Start(ctx)
}

func convOf(m *models.Message) core.ConvID {
	thread := 0
	if m.IsTopicMessage {
		thread = m.MessageThreadID
	}
	return core.ConvID(fmt.Sprintf("%d:%d", m.Chat.ID, thread))
}

// target is where a conversation's messages go.
type target struct {
	chat   int64
	thread int
}

func targetOf(conv core.ConvID) target {
	chat, thread, _ := strings.Cut(string(conv), ":")
	var t target
	t.chat, _ = strconv.ParseInt(chat, 10, 64)
	t.thread, _ = strconv.Atoi(thread)
	return t
}

func userOf(u *models.User) string { return strconv.FormatInt(u.ID, 10) }

func (b *Bot) allowed(userID int64) bool { return slices.Contains(b.cfg.Telegram.AllowedUsers, userID) }

// ---------- updates ----------

func (b *Bot) handleUpdate(ctx context.Context, _ *bot.Bot, u *models.Update) {
	if u.CallbackQuery != nil {
		b.handleCallback(ctx, u.CallbackQuery)
		return
	}
	m := u.Message
	if m == nil || m.From == nil {
		return
	}
	conv := convOf(m)
	if !b.allowed(m.From.ID) {
		log.Printf("ignoring message from unauthorized user %d (@%s)", m.From.ID, m.From.Username)
		b.Notice(conv, fmt.Sprintf("Not authorized. Your Telegram user id is %d.", m.From.ID))
		return
	}

	switch {
	case strings.HasPrefix(m.Text, "/"):
		b.handleCommand(ctx, conv, m)
	// Media is downloaded and processed off the update handler; that work
	// outlives the update, so it doesn't use the handler's context.
	case m.Voice != nil:
		go b.handleVoice(conv, userOf(m.From), m.Voice.FileID, m.Caption) //nolint:gosec // outlives the update handler by design
	case m.Audio != nil:
		go b.handleVoice(conv, userOf(m.From), m.Audio.FileID, m.Caption) //nolint:gosec // outlives the update handler by design
	case len(m.Photo) > 0 || m.Document != nil:
		go b.handleAttachment(conv, m) //nolint:gosec // outlives the update handler by design
	case m.Text != "":
		b.core.Submit(conv, core.Input{User: userOf(m.From), Blocks: []agent.Block{{Text: m.Text}}, Text: m.Text})
	}
}

func (b *Bot) handleVoice(conv core.ConvID, user, fileID, caption string) {
	ctx := context.Background()
	if b.core.Transcriber == nil {
		b.Notice(conv, "🎙 Voice messages are off. Configure a transcriber (see `pocketagent doctor`).")
		return
	}
	status := b.sendHTML(ctx, conv, "🎙 Transcribing…", nil)
	data, name, err := b.download(ctx, fileID)
	var text string
	if err == nil {
		tmp := filepath.Join(os.TempDir(), "pocketagent-voice-"+fmt.Sprint(time.Now().UnixNano())+filepath.Ext(name))
		if err = os.WriteFile(tmp, data, 0o600); err == nil {
			text, err = b.core.Transcriber.Transcribe(ctx, tmp)
			os.Remove(tmp)
		}
	}
	if err != nil {
		b.editHTML(ctx, conv, status, "⚠️ "+html.EscapeString(err.Error()), nil)
		return
	}
	b.editHTML(ctx, conv, status, "🎙 <i>"+html.EscapeString(text)+"</i>", nil)
	if caption != "" {
		text = caption + "\n\n" + text
	}
	b.core.Submit(conv, core.Input{User: user, Blocks: []agent.Block{{Text: text}}, Text: text})
}

var imageTypes = []string{"image/jpeg", "image/png", "image/gif", "image/webp"}

func (b *Bot) handleAttachment(conv core.ConvID, m *models.Message) {
	ctx := context.Background()
	var fileID, origName string
	if len(m.Photo) > 0 {
		fileID = m.Photo[len(m.Photo)-1].FileID // largest size
	} else {
		fileID, origName = m.Document.FileID, m.Document.FileName
	}
	data, name, err := b.download(ctx, fileID)
	if err != nil {
		b.Notice(conv, "⚠️ Couldn't download that file: "+err.Error())
		return
	}
	if origName == "" {
		origName = filepath.Base(name)
	}

	var block agent.Block
	if mt := http.DetectContentType(data); slices.Contains(imageTypes, mt) {
		block = agent.Block{Image: data, MimeType: mt}
	} else {
		dir := filepath.Join(b.cfg.Home, "uploads")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			b.Notice(conv, "⚠️ Couldn't save file: "+err.Error())
			return
		}
		path := filepath.Join(dir, time.Now().Format("20060102-150405")+"-"+filepath.Base(origName))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			b.Notice(conv, "⚠️ Couldn't save file: "+err.Error())
			return
		}
		block = agent.Block{Text: "[The user uploaded a file, saved at " + path + "]"}
	}

	// Albums arrive as separate updates sharing a media_group_id; gather them
	// briefly so they become one prompt.
	if m.MediaGroupID != "" {
		b.mu.Lock()
		al, exists := b.albums[m.MediaGroupID]
		if !exists {
			al = &album{conv: conv, user: userOf(m.From)}
			b.albums[m.MediaGroupID] = al
		}
		al.blocks = append(al.blocks, block)
		if m.Caption != "" {
			al.text = append(al.text, m.Caption)
		}
		b.mu.Unlock()
		if !exists {
			time.AfterFunc(1500*time.Millisecond, func() {
				b.mu.Lock()
				delete(b.albums, m.MediaGroupID)
				blocks, text := al.blocks, strings.Join(al.text, "\n")
				b.mu.Unlock()
				b.submitWithCaption(al.conv, al.user, blocks, text)
			})
		}
		return
	}
	b.submitWithCaption(conv, userOf(m.From), []agent.Block{block}, m.Caption)
}

func (b *Bot) submitWithCaption(conv core.ConvID, user string, blocks []agent.Block, caption string) {
	if caption != "" {
		blocks = append(blocks, agent.Block{Text: caption})
	}
	b.core.Submit(conv, core.Input{User: user, Blocks: blocks, Text: caption})
}

func (b *Bot) download(ctx context.Context, fileID string) ([]byte, string, error) {
	f, err := b.tg.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, "", err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, b.tg.FileDownloadLink(f), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download failed: %s", resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	return data, f.FilePath, err
}

// ---------- core.UI ----------

func (b *Bot) Reply(conv core.ConvID, md string) {
	ctx := context.Background()
	if len(md) > b.cfg.Output.FileThreshold {
		preview := md
		if len(preview) > 600 {
			preview = preview[:600] + "…"
		}
		b.sendMarkdown(ctx, conv, preview)
		logErr("send reply file", b.sendDocument(ctx, conv, "reply.md", []byte(md), "Full reply"))
		return
	}
	b.sendMarkdown(ctx, conv, md)
}

func (b *Bot) Notice(conv core.ConvID, text string) {
	b.sendPlain(context.Background(), conv, text)
}

func (b *Bot) SendFile(ctx context.Context, conv core.ConvID, path, caption string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if slices.Contains([]string{".png", ".jpg", ".jpeg", ".gif", ".webp"}, strings.ToLower(filepath.Ext(path))) {
		_, err = b.tg.SendPhoto(ctx, &bot.SendPhotoParams{
			ChatID: targetOf(conv).chat, MessageThreadID: targetOf(conv).thread, Caption: caption,
			Photo: &models.InputFileUpload{Filename: filepath.Base(path), Data: bytes.NewReader(data)},
		})
		if err == nil {
			return nil
		}
		// Too big or odd dimensions for a photo: send it as a document.
	}
	return b.sendDocument(ctx, conv, filepath.Base(path), data, caption)
}

func (b *Bot) SendVoice(ctx context.Context, conv core.ConvID, ogg []byte) error {
	_, err := b.tg.SendVoice(ctx, &bot.SendVoiceParams{
		ChatID: targetOf(conv).chat, MessageThreadID: targetOf(conv).thread,
		Voice: &models.InputFileUpload{Filename: "reply.ogg", Data: bytes.NewReader(ogg)},
	})
	return err
}

// progress shows tool calls in one message that is edited as work goes on.
type progress struct {
	b        *Bot
	conv     core.ConvID
	stop     chan struct{}
	mu       sync.Mutex
	msg      *models.Message
	lines    []string
	lastEdit time.Time
}

func (b *Bot) StartProgress(conv core.ConvID) core.Progress {
	p := &progress{b: b, conv: conv, stop: make(chan struct{})}
	go func() { // keep "typing…" visible while the agent works
		t := time.NewTicker(4 * time.Second)
		defer t.Stop()
		for {
			_, _ = b.tg.SendChatAction(context.Background(), &bot.SendChatActionParams{ // cosmetic; retried every 4s
				ChatID: targetOf(conv).chat, MessageThreadID: targetOf(conv).thread, Action: models.ChatActionTyping,
			})
			select {
			case <-p.stop:
				return
			case <-t.C:
			}
		}
	}()
	return p
}

func (p *progress) render(footer string) string {
	shown := p.lines
	var sb strings.Builder
	if len(shown) > 8 {
		fmt.Fprintf(&sb, "<i>… %d earlier</i>\n", len(shown)-8)
		shown = shown[len(shown)-8:]
	}
	for _, l := range shown {
		sb.WriteString("🔧 <code>" + html.EscapeString(l) + "</code>\n")
	}
	if footer != "" {
		sb.WriteString("<i>" + html.EscapeString(footer) + "</i>")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func (p *progress) ToolCall(title string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(title) > 150 {
		title = title[:147] + "..."
	}
	p.lines = append(p.lines, title)
	ctx := context.Background()
	if p.msg == nil {
		p.msg = p.b.sendHTML(ctx, p.conv, p.render("working…"), nil)
		p.lastEdit = time.Now()
	} else if time.Since(p.lastEdit) > time.Second { // stay under Telegram's edit rate limit
		p.b.editHTML(ctx, p.conv, p.msg, p.render("working…"), nil)
		p.lastEdit = time.Now()
	}
}

func (p *progress) Flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.msg != nil {
		p.b.editHTML(context.Background(), p.conv, p.msg, p.render(""), nil)
	}
	p.msg, p.lines = nil, nil
}

func (p *progress) Done(summary string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
	ctx := context.Background()
	if p.msg != nil {
		p.b.editHTML(ctx, p.conv, p.msg, p.render(summary), nil)
		return
	}
	_, err := p.b.tg.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: targetOf(p.conv).chat, MessageThreadID: targetOf(p.conv).thread, DisableNotification: true,
		Text: "<i>" + html.EscapeString(summary) + "</i>", ParseMode: models.ParseModeHTML,
	})
	logErr("send summary", err)
}

type approvalView struct {
	b    *Bot
	conv core.ConvID
	id   string
	msg  *models.Message
	text string
}

func (b *Bot) AskApproval(conv core.ConvID, id string, p agent.Permission) core.Approval {
	text := "🔐 <b>" + html.EscapeString(p.Tool) + "</b>"
	if p.Title != "" && p.Title != p.Tool {
		text += "\n" + html.EscapeString(p.Title)
	}
	if p.Detail != "" {
		d := p.Detail
		if len(d) > 2500 {
			d = d[:2500] + "\n…"
		}
		text += "\n<pre>" + html.EscapeString(d) + "</pre>"
	}
	text += "\n<i>Tap a button, or reply with text to deny and tell the agent what to do instead.</i>"

	var row []models.InlineKeyboardButton
	var rows [][]models.InlineKeyboardButton
	for i, o := range p.Options {
		label := o.Label
		if o.Kind.Allows() {
			label = "✅ " + label
		} else {
			label = "❌ " + label
		}
		row = append(row, models.InlineKeyboardButton{Text: label, CallbackData: fmt.Sprintf("ap|%s|%d", id, i)})
		if len(row) == 2 {
			rows, row = append(rows, row), nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}

	b.mu.Lock()
	b.approvals[id] = p.Options
	b.mu.Unlock()
	msg := b.sendHTML(context.Background(), conv, text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
	return &approvalView{b: b, conv: conv, id: id, msg: msg, text: text}
}

func (v *approvalView) Resolve(outcome string) {
	v.b.mu.Lock()
	delete(v.b.approvals, v.id)
	v.b.mu.Unlock()
	text := strings.TrimSuffix(v.text, "\n<i>Tap a button, or reply with text to deny and tell the agent what to do instead.</i>")
	v.b.editHTML(context.Background(), v.conv, v.msg, text+"\n\n<b>"+html.EscapeString(outcome)+"</b>", nil)
}

// ---------- sending ----------

func (b *Bot) sendHTML(ctx context.Context, conv core.ConvID, text string, markup models.ReplyMarkup) *models.Message {
	msg, err := b.tg.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: targetOf(conv).chat, MessageThreadID: targetOf(conv).thread, Text: text, ParseMode: models.ParseModeHTML,
		ReplyMarkup: markup, LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: bot.True()},
	})
	if err != nil {
		log.Printf("send to %s: %v", conv, err)
	}
	return msg
}

func (b *Bot) editHTML(ctx context.Context, conv core.ConvID, msg *models.Message, text string, markup models.ReplyMarkup) {
	if msg == nil {
		b.sendHTML(ctx, conv, text, markup)
		return
	}
	_, err := b.tg.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID: targetOf(conv).chat, MessageID: msg.ID, Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: markup,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: bot.True()},
	})
	logErr("edit message", err)
}

// logErr records failures of best-effort sends that have no caller to
// report to. Re-sending identical text is not a failure.
func logErr(op string, err error) {
	if err != nil && !strings.Contains(err.Error(), "message is not modified") {
		log.Printf("telegram: %s: %v", op, err)
	}
}

func (b *Bot) sendMarkdown(ctx context.Context, conv core.ConvID, md string) {
	for _, chunk := range splitMarkdown(md) {
		_, err := b.tg.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: targetOf(conv).chat, MessageThreadID: targetOf(conv).thread,
			Text: markdownToHTML(chunk), ParseMode: models.ParseModeHTML,
			LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: bot.True()},
		})
		if err != nil {
			// Telegram rejected the HTML; fall back to plain text.
			b.sendPlain(ctx, conv, chunk)
		}
	}
}

func (b *Bot) sendPlain(ctx context.Context, conv core.ConvID, text string) {
	for _, chunk := range splitMarkdown(text) {
		if _, err := b.tg.SendMessage(ctx, &bot.SendMessageParams{ChatID: targetOf(conv).chat, MessageThreadID: targetOf(conv).thread, Text: chunk}); err != nil {
			log.Printf("send to %s: %v", conv, err)
		}
	}
}

func (b *Bot) sendDocument(ctx context.Context, conv core.ConvID, name string, data []byte, caption string) error {
	_, err := b.tg.SendDocument(ctx, &bot.SendDocumentParams{
		ChatID: targetOf(conv).chat, MessageThreadID: targetOf(conv).thread, Caption: caption,
		Document: &models.InputFileUpload{Filename: name, Data: bytes.NewReader(data)},
	})
	return err
}
