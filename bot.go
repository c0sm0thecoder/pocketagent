package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type approvalAnswer struct {
	allow  bool
	always bool
	reason string
}

type pendingApproval struct {
	chatID int64
	ch     chan approvalAnswer
}

type album struct {
	blocks []ContentBlock
	text   []string
}

type App struct {
	cfg    *Config
	store  *Store
	bridge *MCPBridge
	tg     *bot.Bot

	mu        sync.Mutex
	running   map[int64]context.CancelFunc
	approvals map[string]*pendingApproval
	albums    map[string]*album
}

const helpText = `<b>Claude Code over Telegram</b>

Send text, a voice note, or images (with an optional caption). Each chat continues one Claude Code session.

When Claude wants to run a command or edit a file you get Allow/Deny buttons. Reply with text instead of tapping to deny it and tell Claude why.

/new · start a fresh session
/stop · cancel the current run
/cwd [path] · show or change the working directory
/model [name] · show or change the model (e.g. opus, sonnet, default)
/status · session info`

// ---------- update routing ----------

func (a *App) handleUpdate(ctx context.Context, _ *bot.Bot, u *models.Update) {
	if u.CallbackQuery != nil {
		a.handleCallback(ctx, u.CallbackQuery)
		return
	}
	m := u.Message
	if m == nil || m.From == nil {
		return
	}
	if !a.cfg.AllowedUsers[m.From.ID] {
		log.Printf("ignoring message from unauthorized user %d (@%s)", m.From.ID, m.From.Username)
		a.sendPlain(ctx, m.Chat.ID, fmt.Sprintf("Not authorized. Your user id is %d.", m.From.ID))
		return
	}
	chatID := m.Chat.ID

	switch {
	case strings.HasPrefix(m.Text, "/"):
		a.handleCommand(ctx, chatID, m.Text)

	case m.Voice != nil || m.Audio != nil:
		fileID := ""
		if m.Voice != nil {
			fileID = m.Voice.FileID
		} else {
			fileID = m.Audio.FileID
		}
		go a.handleVoice(chatID, fileID, m.Caption)

	case len(m.Photo) > 0 || m.Document != nil:
		go a.handleAttachment(chatID, m)

	case m.Text != "":
		a.submit(chatID, []ContentBlock{{Type: "text", Text: m.Text}}, m.Text)
	}
}

func (a *App) handleCommand(ctx context.Context, chatID int64, text string) {
	cmd, arg, _ := strings.Cut(strings.TrimSpace(text), " ")
	cmd, _, _ = strings.Cut(cmd, "@") // "/new@MyBot" in groups
	arg = strings.TrimSpace(arg)

	switch cmd {
	case "/start", "/help":
		a.sendHTMLRaw(ctx, chatID, helpText)

	case "/new":
		a.store.Update(chatID, func(s *ChatState) { s.SessionID = ""; s.AlwaysAllow = nil })
		a.sendPlain(ctx, chatID, "🆕 Started a new session.")

	case "/stop":
		a.mu.Lock()
		cancel := a.running[chatID]
		a.mu.Unlock()
		if cancel == nil {
			a.sendPlain(ctx, chatID, "Nothing is running.")
			return
		}
		cancel()

	case "/cwd":
		if arg == "" {
			a.sendHTMLRaw(ctx, chatID, "📁 <code>"+html.EscapeString(a.cwd(chatID))+"</code>")
			return
		}
		dir := expandHome(arg)
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(a.cwd(chatID), dir)
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			a.sendPlain(ctx, chatID, "Not a directory: "+dir)
			return
		}
		// Claude Code stores sessions per project directory, so a new cwd needs a new session.
		a.store.Update(chatID, func(s *ChatState) { s.Cwd = filepath.Clean(dir); s.SessionID = ""; s.AlwaysAllow = nil })
		a.sendHTMLRaw(ctx, chatID, "📁 Now in <code>"+html.EscapeString(filepath.Clean(dir))+"</code> (new session)")

	case "/model":
		if arg == "" {
			a.sendPlain(ctx, chatID, "Model: "+a.model(chatID))
			return
		}
		if arg == "default" {
			arg = ""
		}
		a.store.Update(chatID, func(s *ChatState) { s.Model = arg })
		a.sendPlain(ctx, chatID, "Model: "+a.model(chatID))

	case "/status":
		st := a.store.Get(chatID)
		a.mu.Lock()
		busy := a.running[chatID] != nil
		a.mu.Unlock()
		session := st.SessionID
		if session == "" {
			session = "(new)"
		}
		always := "none"
		if len(st.AlwaysAllow) > 0 {
			always = strings.Join(st.AlwaysAllow, ", ")
		}
		a.sendHTMLRaw(ctx, chatID, fmt.Sprintf(
			"<b>Status</b>\nRunning: %v\nCwd: <code>%s</code>\nSession: <code>%s</code>\nModel: %s\nAlways allowed: %s\nSpent in this chat: $%.4f",
			busy, html.EscapeString(a.cwd(chatID)), session, html.EscapeString(a.model(chatID)), html.EscapeString(always), st.TotalCost))

	default:
		// Not one of ours; let Claude see it (e.g. "/compact" style text).
		a.submit(chatID, []ContentBlock{{Type: "text", Text: text}}, text)
	}
}

func (a *App) cwd(chatID int64) string {
	if c := a.store.Get(chatID).Cwd; c != "" {
		return c
	}
	return a.cfg.DefaultCwd
}

func (a *App) model(chatID int64) string {
	if m := a.store.Get(chatID).Model; m != "" {
		return m
	}
	if a.cfg.ClaudeModel != "" {
		return a.cfg.ClaudeModel
	}
	return "default"
}

// ---------- inputs ----------

func (a *App) handleVoice(chatID int64, fileID, caption string) {
	ctx := context.Background()
	status, _ := a.tg.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "🎙 Transcribing…"})

	data, name, err := a.download(ctx, fileID)
	var text string
	if err == nil {
		tmp := filepath.Join(os.TempDir(), "tg-"+randomID()+filepath.Ext(name))
		if err = os.WriteFile(tmp, data, 0o600); err == nil {
			text, err = transcribe(ctx, a.cfg, tmp)
			os.Remove(tmp)
		}
	}
	if err != nil {
		a.editOrSend(ctx, chatID, status, "⚠️ "+html.EscapeString(err.Error()))
		return
	}
	a.editOrSend(ctx, chatID, status, "🎙 <i>"+html.EscapeString(text)+"</i>")

	if caption != "" {
		text = caption + "\n\n" + text
	}
	a.submit(chatID, []ContentBlock{{Type: "text", Text: text}}, text)
}

var supportedImageTypes = []string{"image/jpeg", "image/png", "image/gif", "image/webp"}

func (a *App) handleAttachment(chatID int64, m *models.Message) {
	ctx := context.Background()

	var fileID, origName string
	if len(m.Photo) > 0 {
		fileID = m.Photo[len(m.Photo)-1].FileID // largest size
	} else {
		fileID, origName = m.Document.FileID, m.Document.FileName
	}

	data, name, err := a.download(ctx, fileID)
	if err != nil {
		a.sendPlain(ctx, chatID, "⚠️ Couldn't download that file: "+err.Error())
		return
	}
	if origName == "" {
		origName = filepath.Base(name)
	}

	var block ContentBlock
	if mt := http.DetectContentType(data); slices.Contains(supportedImageTypes, mt) {
		block = ContentBlock{Type: "image", Source: &ImageSource{
			Type: "base64", MediaType: mt, Data: base64.StdEncoding.EncodeToString(data),
		}}
	} else {
		// Not an image Claude can see directly: save it and tell Claude where it is.
		dir := filepath.Join(a.cfg.DataDir, "uploads")
		os.MkdirAll(dir, 0o700)
		path := filepath.Join(dir, time.Now().Format("20060102-150405")+"-"+filepath.Base(origName))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			a.sendPlain(ctx, chatID, "⚠️ Couldn't save file: "+err.Error())
			return
		}
		block = ContentBlock{Type: "text", Text: "[The user uploaded a file, saved at " + path + "]"}
	}

	// Albums arrive as separate updates that share a media_group_id.
	// Collect them for a moment so they become one prompt.
	if m.MediaGroupID != "" {
		a.mu.Lock()
		al, exists := a.albums[m.MediaGroupID]
		if !exists {
			al = &album{}
			a.albums[m.MediaGroupID] = al
		}
		al.blocks = append(al.blocks, block)
		if m.Caption != "" {
			al.text = append(al.text, m.Caption)
		}
		a.mu.Unlock()
		if !exists {
			time.AfterFunc(1500*time.Millisecond, func() {
				a.mu.Lock()
				delete(a.albums, m.MediaGroupID)
				a.mu.Unlock()
				a.submitWithCaption(chatID, al.blocks, strings.Join(al.text, "\n"))
			})
		}
		return
	}
	a.submitWithCaption(chatID, []ContentBlock{block}, m.Caption)
}

func (a *App) submitWithCaption(chatID int64, blocks []ContentBlock, caption string) {
	if caption != "" {
		blocks = append(blocks, ContentBlock{Type: "text", Text: caption})
	}
	a.submit(chatID, blocks, caption)
}

func (a *App) download(ctx context.Context, fileID string) ([]byte, string, error) {
	f, err := a.tg.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, "", err
	}
	resp, err := http.Get(a.tg.FileDownloadLink(f))
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

// ---------- running claude ----------

// submit starts a Claude turn for this chat. If one is already running and
// waiting on an approval, a text message denies it with that text as the reason.
func (a *App) submit(chatID int64, blocks []ContentBlock, text string) {
	ctx := context.Background()
	a.mu.Lock()
	if a.running[chatID] != nil {
		var pending *pendingApproval
		for _, p := range a.approvals {
			if p.chatID == chatID {
				pending = p
				break
			}
		}
		a.mu.Unlock()
		if pending != nil && text != "" {
			select {
			case pending.ch <- approvalAnswer{reason: "The user denied this and said: " + text}:
			default:
			}
			return
		}
		a.sendPlain(ctx, chatID, "⏳ Still working on the previous message. Send /stop to cancel it.")
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	a.running[chatID] = cancel
	a.mu.Unlock()

	go func() {
		defer func() {
			cancel()
			a.mu.Lock()
			delete(a.running, chatID)
			a.mu.Unlock()
		}()
		a.run(runCtx, chatID, blocks)
	}()
}

func (a *App) run(ctx context.Context, chatID int64, blocks []ContentBlock) {
	start := time.Now()
	bg := context.Background() // Telegram calls must still work after /stop

	// Keep the "typing…" indicator alive while Claude works.
	go func() {
		t := time.NewTicker(4 * time.Second)
		defer t.Stop()
		for {
			a.tg.SendChatAction(bg, &bot.SendChatActionParams{ChatID: chatID, Action: models.ChatActionTyping})
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	var (
		activity    *models.Message
		toolLines   []string
		toolCount   int
		sentText    bool
		lastEdit    time.Time
		renderTools = func(header string) string {
			shown := toolLines
			if len(shown) > 8 {
				shown = shown[len(shown)-8:]
			}
			var sb strings.Builder
			sb.WriteString(header)
			for _, l := range shown {
				sb.WriteString("\n🔧 <code>" + html.EscapeString(l) + "</code>")
			}
			return sb.String()
		}
	)

	events := RunEvents{
		OnSession: func(id string) {
			a.store.Update(chatID, func(s *ChatState) { s.SessionID = id })
		},
		OnText: func(text string) {
			sentText = true
			if activity != nil { // flush any throttled tool lines first
				a.editHTML(bg, chatID, activity.ID, renderTools(""))
			}
			a.sendMarkdown(bg, chatID, text)
			activity = nil // later tool calls start a fresh activity message below the text
			toolLines = nil
		},
		OnToolUse: func(name string, raw json.RawMessage) {
			toolCount++
			var input map[string]any
			json.Unmarshal(raw, &input)
			toolLines = append(toolLines, toolSummary(name, input))
			body := renderTools("<i>Working…</i>")
			if activity == nil {
				activity, _ = a.tg.SendMessage(bg, &bot.SendMessageParams{ChatID: chatID, Text: body, ParseMode: models.ParseModeHTML})
				lastEdit = time.Now()
			} else if time.Since(lastEdit) > time.Second { // stay under Telegram's edit rate limit
				a.editHTML(bg, chatID, activity.ID, body)
				lastEdit = time.Now()
			}
		},
	}

	st := a.store.Get(chatID)
	opts := RunOptions{
		Bin:            a.cfg.ClaudeBin,
		Cwd:            a.cwd(chatID),
		SessionID:      st.SessionID,
		Model:          st.Model,
		AllowedTools:   append(slices.Clone(a.cfg.AutoAllowTools), st.AlwaysAllow...),
		MCPConfig:      a.bridge.ConfigFor(chatID),
		PermissionTool: permissionToolName,
	}
	if opts.Model == "" {
		opts.Model = a.cfg.ClaudeModel
	}

	res, err := runClaude(ctx, opts, blocks, events)
	if errors.Is(err, errNoConversation) {
		a.store.Update(chatID, func(s *ChatState) { s.SessionID = "" })
		a.sendPlain(bg, chatID, "ℹ️ Previous session not found, starting a new one.")
		opts.SessionID = ""
		res, err = runClaude(ctx, opts, blocks, events)
	}

	if activity != nil {
		a.editHTML(bg, chatID, activity.ID, renderTools(""))
	}

	switch {
	case errors.Is(err, context.Canceled):
		a.sendPlain(bg, chatID, "⏹ Stopped.")
		return
	case err != nil:
		a.sendPlain(bg, chatID, "⚠️ "+err.Error())
		return
	}

	a.store.Update(chatID, func(s *ChatState) { s.TotalCost += res.CostUSD })
	if res.IsError || !sentText {
		if res.Text != "" {
			a.sendMarkdown(bg, chatID, res.Text)
		}
	}
	footer := fmt.Sprintf("✓ %s · $%.4f", time.Since(start).Round(time.Second), res.CostUSD)
	if toolCount > 0 {
		footer += fmt.Sprintf(" · %d tool calls", toolCount)
	}
	a.tg.SendMessage(bg, &bot.SendMessageParams{
		ChatID: chatID, Text: "<i>" + footer + "</i>", ParseMode: models.ParseModeHTML, DisableNotification: true,
	})
}

// ---------- approvals (ChatUI) ----------

func (a *App) AskApproval(ctx context.Context, chatID int64, tool string, input map[string]any) (bool, string) {
	if slices.Contains(a.store.Get(chatID).AlwaysAllow, tool) {
		return true, ""
	}

	id := randomID()
	p := &pendingApproval{chatID: chatID, ch: make(chan approvalAnswer, 1)}
	a.mu.Lock()
	a.approvals[id] = p
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.approvals, id)
		a.mu.Unlock()
	}()

	prompt := "🔐 Claude wants to use <b>" + html.EscapeString(tool) + "</b>\n" + describeToolInput(tool, input)
	msg, err := a.tg.SendMessage(context.Background(), &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      prompt,
		ParseMode: models.ParseModeHTML,
		ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{
				{Text: "✅ Allow", CallbackData: "ap|" + id + "|y"},
				{Text: "❌ Deny", CallbackData: "ap|" + id + "|n"},
			},
			{{Text: "✅ Always allow " + tool + " in this chat", CallbackData: "ap|" + id + "|a"}},
		}},
	})
	if err != nil {
		return false, "Could not reach the user on Telegram to ask for approval."
	}

	var ans approvalAnswer
	var outcome string
	select {
	case ans = <-p.ch:
		switch {
		case ans.always:
			outcome = "✅ Always allowed"
		case ans.allow:
			outcome = "✅ Allowed"
		default:
			outcome = "❌ Denied"
		}
	case <-ctx.Done():
		ans, outcome = approvalAnswer{reason: "Cancelled."}, "⏹ Cancelled"
	case <-time.After(a.cfg.ApprovalTimeout):
		ans, outcome = approvalAnswer{reason: "The user did not respond in time."}, "⌛ Timed out"
	}

	if ans.always {
		a.store.Update(chatID, func(s *ChatState) {
			if !slices.Contains(s.AlwaysAllow, tool) {
				s.AlwaysAllow = append(s.AlwaysAllow, tool)
			}
		})
	}
	a.editHTML(context.Background(), chatID, msg.ID, prompt+"\n\n<b>"+outcome+"</b>")
	return ans.allow, ans.reason
}

func (a *App) handleCallback(ctx context.Context, q *models.CallbackQuery) {
	answer := func(text string) {
		a.tg.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: text})
	}
	if !a.cfg.AllowedUsers[q.From.ID] {
		answer("Not authorized")
		return
	}
	parts := strings.Split(q.Data, "|")
	if len(parts) != 3 || parts[0] != "ap" {
		answer("")
		return
	}
	a.mu.Lock()
	p := a.approvals[parts[1]]
	a.mu.Unlock()
	if p == nil {
		answer("This request has expired.")
		return
	}
	var ans approvalAnswer
	switch parts[2] {
	case "y":
		ans.allow = true
	case "a":
		ans.allow, ans.always = true, true
	}
	select {
	case p.ch <- ans:
	default:
	}
	answer("")
}

func describeToolInput(tool string, input map[string]any) string {
	str := func(k string) string { s, _ := input[k].(string); return s }
	pre := func(s string, max int) string {
		if len(s) > max {
			s = s[:max] + "\n…"
		}
		return "<pre>" + html.EscapeString(s) + "</pre>"
	}
	switch tool {
	case "Bash":
		out := pre(str("command"), 2500)
		if d := str("description"); d != "" {
			out = "<i>" + html.EscapeString(d) + "</i>\n" + out
		}
		return out
	case "Edit":
		return "<code>" + html.EscapeString(str("file_path")) + "</code>\n− " + pre(str("old_string"), 1200) + "+ " + pre(str("new_string"), 1200)
	case "Write":
		return "<code>" + html.EscapeString(str("file_path")) + "</code>\n" + pre(str("content"), 2000)
	case "WebFetch":
		return "<code>" + html.EscapeString(str("url")) + "</code>"
	}
	data, _ := json.MarshalIndent(input, "", "  ")
	return pre(string(data), 2500)
}

// ---------- sending ----------

var imageExts = []string{".png", ".jpg", ".jpeg", ".gif", ".webp"}

func (a *App) SendFile(ctx context.Context, chatID int64, path, caption string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	upload := func() *models.InputFileUpload {
		return &models.InputFileUpload{Filename: filepath.Base(path), Data: bytes.NewReader(data)}
	}
	if slices.Contains(imageExts, strings.ToLower(filepath.Ext(path))) {
		_, err = a.tg.SendPhoto(ctx, &bot.SendPhotoParams{ChatID: chatID, Photo: upload(), Caption: caption})
		if err == nil {
			return nil
		}
		// Too large or odd dimensions for a photo; fall back to a document.
	}
	_, err = a.tg.SendDocument(ctx, &bot.SendDocumentParams{ChatID: chatID, Document: upload(), Caption: caption})
	return err
}

func (a *App) sendMarkdown(ctx context.Context, chatID int64, md string) {
	for _, chunk := range splitMarkdown(md) {
		_, err := a.tg.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:             chatID,
			Text:               markdownToHTML(chunk),
			ParseMode:          models.ParseModeHTML,
			LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: bot.True()},
		})
		if err != nil {
			// Telegram rejected our HTML; send the raw text instead.
			a.sendPlain(ctx, chatID, chunk)
		}
	}
}

func (a *App) sendPlain(ctx context.Context, chatID int64, text string) {
	for _, chunk := range splitMarkdown(text) {
		if _, err := a.tg.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: chunk}); err != nil {
			log.Printf("send to %d: %v", chatID, err)
		}
	}
}

func (a *App) sendHTMLRaw(ctx context.Context, chatID int64, text string) {
	if _, err := a.tg.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text, ParseMode: models.ParseModeHTML}); err != nil {
		log.Printf("send to %d: %v", chatID, err)
	}
}

func (a *App) editHTML(ctx context.Context, chatID int64, msgID int, text string) {
	a.tg.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID: chatID, MessageID: msgID, Text: text, ParseMode: models.ParseModeHTML})
}

func (a *App) editOrSend(ctx context.Context, chatID int64, msg *models.Message, text string) {
	if msg != nil {
		a.editHTML(ctx, chatID, msg.ID, text)
		return
	}
	a.sendHTMLRaw(ctx, chatID, text)
}

func randomID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}
