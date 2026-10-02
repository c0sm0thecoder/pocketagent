# Contributing

Thanks for helping! Bug reports, agent configs that work for you, and PRs are all welcome.

## Design rules

- **No vendor is special.** Defaults, ordering and naming stay neutral. Vendor names appear only where something really is specific to a vendor: an adapter's own package, or a preset.
- **Depend on small interfaces, declared by the consumer.** The core declares `UI`, `Store`, `ToolHost` and `Checkpointer`; adapters depend only on `internal/agent`.
- **Optional abilities are optional interfaces.** An agent implements `agent.Agent`, plus `agent.ModelLister`, `agent.ToolProvider` or `io.Closer` only if it can. Speech providers can implement `Check() error` for `doctor`.
- **Implementations own their options.** The config passes unknown keys through as `Options`; each adapter or provider decodes them strictly into its own struct.
- **One composition root.** `cmd/pocketagent/run.go` builds and wires everything; `internal/registry` is the only place that names concrete implementations.

## Layout

```
cmd/pocketagent            CLI: run, start/stop, init, doctor, service
internal/agent             the Agent contract
internal/agent/acp         Agent Client Protocol adapter
internal/agent/claudecode  Claude Code CLI adapter (stream-json + a permission-prompt tool)
internal/agent/command     any CLI via argv templates
internal/argv              {placeholder} expansion, single pass, no shell
internal/bridge            MCP tool server; per-agent namespaces
internal/config            YAML config; implementation options stay opaque
internal/core              conversations, queue, permission policy, budgets, checkpoints
internal/frontend/telegram Telegram frontend; implements core.UI
internal/gitcp             git snapshots for /diff and /undo
internal/registry          type name to implementation, plus agent presets for init
internal/store             JSON state
internal/stt, internal/tts speech providers
internal/term              terminal output cleanup
```

## Adding an agent

1. If it speaks ACP, no code is needed: add a preset to `internal/registry/presets.go` (sorted by name) and the same entry to `config.example.yaml`. `TestPresetsMatchExample` keeps the two in sync.
2. If it's a plain CLI, a `command` preset usually does the job.
3. Write a new adapter only if neither covers it: implement `agent.Agent` in `internal/agent/<name>`, decode your options with `spec.Options.Decode`, report honest `Caps`, and register the type in `internal/registry/registry.go`.

## Adding a speech provider

Write a `func(Options) (Transcriber, error)` (or `Speaker`) in `internal/stt` or `internal/tts`. Implement `Check() error` if it needs local tools, and register it in `internal/registry`.

## Tests

```sh
go test -race ./...                 # must pass; no network or agents needed
go test -tags integration ./...     # real agents; needs them installed and logged in
```

The core runs against in-memory fakes, the frontend against a fake Bot API, and the bridge against a real MCP client. Please add tests next to your change.

## Style

`gofmt`, `go vet`, small packages, comments that explain why. Keep the dependency list short.
