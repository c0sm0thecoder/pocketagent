# claude-telegram

Talk to Claude Code from Telegram, using text, voice notes or images.

Each Telegram chat maps to one Claude Code session that runs on your machine. When Claude wants to run a command or edit a file, you get **Allow / Deny** buttons in the chat.

## How it works

```
Telegram ──► bot (Go) ──► claude -p --input-format stream-json --resume <session>
   ▲            │                 │
   │            │   voice: ffmpeg → whisper.cpp → text
   │            │   images: base64 image blocks
   │            ▼                 ▼
   └──── buttons/files ◄── local MCP server (127.0.0.1)
                            • approve   → --permission-prompt-tool
                            • send_file → Claude can send you screenshots/files
```

* Each message runs one `claude -p` turn and resumes the chat's session, so context carries over.
* Replies stream back as they arrive. Tool calls show up in one compact "Working…" message.
* Permission prompts go through a localhost MCP server (bearer-token protected). Tap **Allow**, **Deny** or **Always allow <tool> in this chat**, or *reply with text* to deny the action and tell Claude what to do instead.
* State (session id, cwd, model, always-allowed tools, cost) lives in `~/.claude-telegram/state.json`.

## Setup

```sh
brew install ffmpeg whisper-cpp
mkdir -p ~/.claude-telegram/models
curl -L -o ~/.claude-telegram/models/ggml-small.bin \
  https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small.bin

cp .env.example .env   # fill in TELEGRAM_BOT_TOKEN and ALLOWED_USER_IDS
go build -o claude-telegram . && ./claude-telegram
```

1. Create a bot with [@BotFather](https://t.me/BotFather) (`/newbot`) and copy the token.
2. Get your numeric user id from [@userinfobot](https://t.me/userinfobot). If you skip this, the bot replies with your id when you message it.
3. Make sure `claude` is logged in on this machine (`claude` once interactively).

## Commands

| | |
|---|---|
| `/new` | fresh session (also clears "always allow") |
| `/stop` | kill the current run |
| `/cwd [path]` | show or change working directory (starts a new session) |
| `/model [name]` | `opus`, `sonnet`, `haiku`, or `default` |
| `/status` | session, cwd, model, spend |

## Security notes

* **Only ids in `ALLOWED_USER_IDS` can use the bot.** Anyone else is ignored. Without an allowlist this would be a remote shell on your Mac.
* Your existing Claude Code permission rules still apply. Anything they already allow (including read-only Bash commands like `ls` or `echo`, which Claude Code auto-approves) will not ask you.
* `Read` is auto-allowed by default, so Claude can read any file your user can. Remove it from `AUTO_ALLOW_TOOLS` if that is too loose.
* "Always allow Bash" in a chat means every command runs unprompted until `/new`.

## Tests

```sh
go test ./...                                  # formatting
go test -tags integration -v ./...             # real claude + whisper (costs a few cents)
```

## Running in the background (macOS)

Use a LaunchAgent, or just `tmux new -d -s tg ./claude-telegram`.
