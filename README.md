# pocketagent

Your coding agent in your pocket. Drive Claude Code (and soon Codex, Gemini, Kiro, Aider and any other CLI agent) from Telegram with text, voice notes and images. Approve risky actions with a tap.

> Work in progress. See the [releases](https://github.com/c0sm0thecoder/pocketagent/releases) for what's shipped.

## Quick start

```sh
go install github.com/c0sm0thecoder/pocketagent/cmd/pocketagent@latest
mkdir -p ~/.pocketagent && cp config.example.yaml ~/.pocketagent/config.yaml   # then edit it
pocketagent run
```

You need a bot token from [@BotFather](https://t.me/BotFather) and your numeric Telegram user id (from [@userinfobot](https://t.me/userinfobot)).

## Development

```sh
go test ./...                                   # unit tests
go test -tags integration ./internal/agent/...  # against real agent CLIs (costs a few cents)
```

## License

MIT
