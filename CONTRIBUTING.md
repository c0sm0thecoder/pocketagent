# Contributing

Thanks for helping! Bug reports, agent configs that work for you, and PRs are all welcome.

## Layout

```
cmd/pocketagent        CLI: run, start/stop, init, doctor, service
internal/config        YAML config, defaults, validation
internal/core          conversations, queue, permission policy, budgets, checkpoints (no Telegram code)
internal/telegram      the Telegram frontend; implements core.UI
internal/agent         the Agent interface
internal/agent/claude  Claude Code CLI (stream-json + MCP permission tool)
internal/agent/acp     Agent Client Protocol client
internal/agent/command any CLI via argv templates
internal/bridge        localhost MCP server: approve, send_file
internal/stt, tts      speech providers
internal/gitcp         git snapshots for /diff and /undo
```

## Adding an agent

1. If it speaks ACP, you don't need code: add it to `config.example.yaml`, the README table, and `knownAgents` in `cmd/pocketagent/setup.go` so `init` detects it.
2. If it's a plain CLI, a `command` config usually does the job.
3. Only write a new adapter if the agent needs something neither covers. Implement `agent.Agent` and report honest `Caps`.

## Tests

```sh
go test -race ./...                 # must pass, no network needed
go test -tags integration ./...     # runs real agents; needs them installed and logged in
```

The core and frontend are tested against fakes (`internal/core/core_test.go`, and `internal/telegram/telegram_test.go`, which runs a fake Bot API). Please add tests next to your change.

## Style

`gofmt`, `go vet`, small packages, comments that explain why. Keep the dependency list short.
