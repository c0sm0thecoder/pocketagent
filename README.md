# pocketagent

**Your coding agent in your pocket.** Talk to Claude Code, Codex, Gemini, Kiro, Aider or any other coding agent from Telegram: type, send a voice note, or drop in a screenshot. When the agent wants to run a command or edit a file, approve it with a tap.

It runs on your own machine, next to your code, as one small Go binary.

```
you (Telegram) ──► pocketagent ──► claude / codex / gemini / kiro / aider / ...
  text, voice,       queue, approvals,          on your machine, in your repos
  images             checkpoints, budgets
```

## Features

- **Any agent.** Claude Code natively; Codex, Gemini, Kiro, Goose, Copilot, Cursor, OpenCode and more through the [Agent Client Protocol](https://agentclientprotocol.com); anything else (Aider, your own scripts) through a command template.
- **Voice in and out.** Voice notes are transcribed locally with whisper.cpp, or by any OpenAI-compatible API (OpenAI, Groq, a self-hosted server). `/voice` reads replies back to you.
- **Images.** Send screenshots and photos, or whole albums. Agents can send files and images back.
- **Approvals with one tap.** Allow, Always allow, or Deny. Or reply with text to deny and tell the agent what to do instead. Four modes: `ask`, `edits`, `plan`, `yolo`.
- **One session per chat or forum topic.** Run several agents on several projects in parallel, one topic each.
- **Switch with a tap.** `/agent`, `/model`, `/mode`, `/project` and `/sessions` show inline pickers.
- **Undo.** Every turn takes a git snapshot (without touching your branch, index or stash). `/diff` shows what changed, and `/undo` rolls it back.
- **Queue.** Keep sending messages while the agent works; they run in order.
- **Budgets.** A daily spend cap per user, for agents that report cost.
- **Sandboxing.** Wrap any agent in a container (see [docs/sandbox.md](docs/sandbox.md)).
- **Runs itself.** `init` sets everything up, `doctor` checks it, and `service install` keeps it running.

## Quick start

**1. Install** (macOS or Linux):

```sh
# Homebrew
brew install c0sm0thecoder/tap/pocketagent

# or with Go
go install github.com/c0sm0thecoder/pocketagent/cmd/pocketagent@latest

# or download a binary from the releases page
```

You also need at least one agent installed and logged in (for example [Claude Code](https://docs.claude.com/en/docs/claude-code) or [Codex](https://github.com/openai/codex)). For local voice transcription: `brew install whisper-cpp ffmpeg`.

**2. Set up:**

```sh
pocketagent init
```

It asks for a bot token (message [@BotFather](https://t.me/BotFather), send `/newbot`), then waits for you to message your new bot so it can lock the bot to your account. It then detects the agents you have installed and sets up voice.

**3. Run:**

```sh
pocketagent start             # background
pocketagent service install   # or: start at login, restart on crashes
```

Then message your bot.

## Using it

| | |
|---|---|
| text / voice / photo | sent to the agent; captions become the prompt |
| `/agent` `/model` `/mode` `/project` | switch with inline buttons (or `/model sonnet`) |
| `/new` | fresh session |
| `/sessions` | resume a recent session |
| `/stop` | cancel the run and anything queued |
| `/diff` | changes from the last turn (`/diff all`: since HEAD) |
| `/undo` | roll back the last turn's file changes |
| `/cwd [path]` | show or change the working directory |
| `/voice` | toggle spoken replies |
| `/status`, `/usage` | settings, spend |

Any other `/command` is passed to the agent.

**Parallel work:** create a Telegram group, turn on *Topics*, and add your bot. Each topic is its own conversation with its own agent, project and session.

### Permission modes

| mode | what runs without asking |
|---|---|
| `ask` (default) | tool kinds in `auto_allow` (read, search, think) |
| `edits` | plus file edits |
| `plan` | nothing changes; the agent plans (if it supports plan mode) |
| `yolo` | everything |

"Always allow" remembers a tool for the current session; `/new` forgets it.

## Agents

| agent | type | command |
|---|---|---|
| Claude Code | `claude` | `claude` |
| Codex | `acp` | `npx -y @zed-industries/codex-acp` |
| Gemini CLI | `acp` | `gemini --acp` |
| Kiro | `acp` | `kiro-cli acp` |
| Goose | `acp` | `goose acp` |
| GitHub Copilot | `acp` | `copilot --acp --stdio` |
| Cursor | `acp` | `cursor-agent acp` |
| OpenCode | `acp` | `opencode acp` |
| Claude Code via ACP | `acp` | `npx -y @agentclientprotocol/claude-agent-acp` |
| Aider, scripts | `command` | `aider --message "{prompt}" --yes-always ...` |

`claude` drives the Claude Code CLI directly and reports cost. `acp` works with any ACP agent: approvals, images, models and modes come through the protocol. `command` runs any CLI with `{prompt}`, `{cwd}`, `{model}` and `{path}` placeholders.

## Configuration

The config lives in `~/.pocketagent/config.yaml` (or `$POCKETAGENT_HOME`). [config.example.yaml](config.example.yaml) documents every option. Values like `${GROQ_API_KEY}` are read from the environment.

```yaml
defaults: { agent: claude, cwd: ~/projects, mode: ask }
agents:
  claude: { type: claude, models: [opus, sonnet, haiku] }
  gemini: { type: acp, command: [gemini, --acp] }
projects:
  api: { cwd: ~/work/api, agent: gemini, mode: edits }
transcriber: { type: openai, base_url: https://api.groq.com/openai/v1, api_key: "${GROQ_API_KEY}", model: whisper-large-v3-turbo }
tts: { type: say }
budget: { daily_usd: 5 }
```

### Running on a server

A container image with pocketagent, Claude Code, Codex and Gemini preinstalled:

```sh
# the volume keeps your config, sessions and agent logins
docker run -it --rm -v pocketagent:/home/agent ghcr.io/c0sm0thecoder/pocketagent init
docker run -it --rm -v pocketagent:/home/agent --entrypoint claude ghcr.io/c0sm0thecoder/pocketagent   # log in once
docker run -d --name pocketagent --restart unless-stopped \
  -v pocketagent:/home/agent -v ~/code:/home/agent/code \
  ghcr.io/c0sm0thecoder/pocketagent
```

## Security

pocketagent lets the people in `telegram.allowed_users` run a coding agent on your machine. Read [SECURITY.md](SECURITY.md). In short:

- It won't start without an allowlist, and it ignores everyone else.
- Approvals are on by default. Use `yolo` and "always allow" with care, or run the agent in a [sandbox](docs/sandbox.md).
- Your bot token is a password. Keep the config private (`init` writes it with mode 600). If the token leaks, revoke it with @BotFather.

## Development

```sh
go test ./...                                   # unit tests (no network, no agents)
go test -tags integration ./...                 # real agents, whisper and Docker (costs a few cents)
go run ./cmd/pocketagent doctor
```

See [CONTRIBUTING.md](CONTRIBUTING.md), including how to add an agent.

## License

[MIT](LICENSE)
