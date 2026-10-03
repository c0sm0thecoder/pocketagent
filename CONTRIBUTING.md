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

## Development workflow

```sh
brew install shellcheck hadolint   # once (or your package manager)
make hooks     # once: pre-commit (secret scan, lint) and pre-push (full gate)
make check     # everything CI runs, in about 30 seconds
make help      # all targets
```

`make check` is the single definition of "green", used by the git hooks, CI and the release workflow:

| target | what it guards against |
|---|---|
| `lint` | bugs and security problems (golangci-lint with gosec, errcheck, errorlint, staticcheck, ...) and broken workflows (actionlint with shellcheck) |
| `dockerlint` | Dockerfile problems (hadolint) |
| `vet` | suspicious code, including the integration tests |
| `cover` | regressions, with coverage thresholds: internal/ total at least 78%, every internal package at least 60% (packages without tests count as 0%) |
| `vuln` | known vulnerabilities in code we actually call and in every tool binary, including the Go standard library (govulncheck) |
| `secrets` | credentials anywhere in the git history (gitleaks) |
| `tidy` | go.mod/go.sum drift, and tools built with the same Go toolchain as the module |
| `build` | every release target still compiles |

Tool versions are pinned in `tools/<name>/go.mod` and run with `go tool`, so everyone, CI included, uses the same versions. Dependabot keeps them, the Go modules, the GitHub Actions (pinned by commit SHA) and the Docker base images up to date.

CI adds CodeQL, dependency review on pull requests, a build of both container images, and an OpenSSF Scorecard.

## Tests

```sh
make test                           # unit tests, race detector on
make integration                    # real agents, whisper and Docker; costs a few cents
go test ./internal/frontend/telegram -fuzz=FuzzMarkdownToHTML   # fuzz a target (see *_fuzz_test.go)
```

The core runs against in-memory fakes, the Telegram frontend against a fake Bot API, the bridge against a real MCP client, and the ACP and Claude Code adapters against fake agents (the test binary re-runs itself as the agent). Please add tests next to your change.

## Style

`gofmt`, `go vet`, small packages, comments that explain why. Keep the dependency list short.
