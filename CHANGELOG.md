# Changelog

All notable changes are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/). Until 1.0, minor versions may change the config format; such changes are called out with migration notes.

## [Unreleased]

## [0.9.0] - 2026-10-04

### Added
- Project discovery: `pocketagent init` now has a Projects step that suggests folders from your code directories (`~/projects`, `~/code`, ...) and from where your agents ran recently, and `/topics` with no projects (or the new 🔎 *Find projects* button in `/project`) offers the same suggestions as buttons, including "Add all and create topics". Agent history locations are data (Claude Code and Codex today); folders are de-duplicated by identity, so case-insensitive file systems don't show duplicates, and subfolders fold into their project unless they are their own git repo.
- `init` ends with the recommended group setup, and `/start` explains it.
- README: a "Take off in five minutes" section built around the one-topic-per-project workbench.
- `/topics` creates a forum topic for every project (or the named ones) in a group with Topics, binds each topic to its project and posts a short welcome. Projects that already have a topic are skipped.

## [0.8.0] - 2026-10-04

### Added
- `/project add [name] [path]` registers an existing folder as a project from chat, and the `/project` picker offers "➕ Add this folder". `/project remove <name>` forgets one. Chat-added projects live in the state file; projects in the config file take precedence and can't be removed from chat.
- The `/project` picker shows each project's folder.

## [0.7.0] - 2026-10-03

### Added
- Quality gate shared by git hooks and CI (`make check`): golangci-lint with gosec, actionlint with shellcheck, hadolint, race tests with coverage thresholds, govulncheck, gitleaks over the full history, module tidiness and cross-builds.
- CodeQL, dependency review, OpenSSF Scorecard and Dependabot.
- Unit tests for the ACP and Claude Code adapters (against fake agents), the Telegram frontend, speech providers, the store and the CLI. Library coverage is now 80%.
- Community files: code of conduct, issue and pull request templates.
- Fuzz tests for the Markdown-to-HTML converter, message splitting, argv expansion and the config parser.
- Signed releases: Sigstore signature on the checksums and GitHub build provenance for every archive (see SECURITY.md).
- `make vuln` also scans every development tool binary; tools are built with the same pinned Go toolchain.

### Changed
- Go 1.26.8 (Go 1.25 is out of support and had 13 reachable standard-library vulnerabilities).
- Container images: Go 1.26, Node 24 LTS, numeric user, base images pinned by digest.

### Fixed
- The tool server had no timeouts.
- The whisper model download could keep a truncated file.
- ACP: a failed mode switch (for example to plan mode) was ignored silently; it is now reported.
- ACP: `Close` panicked when called twice.
- systemd unit: `PATH` with spaces broke the service.
- Close errors on written files are now checked (CodeQL).

## [0.6.0] - 2026-10-02

### Changed
- Vendor-neutral, interface-driven architecture. **Config changes:** agent type `claude` is now `claude-code`, speech type `openai` is now `http`, mode `yolo` is now `full`, and `auto_allow`/`approval_timeout` moved under `permissions`.

### Fixed
- `{placeholders}` typed in a prompt were expanded inside command templates.

## [0.5.1] - 2026-10-01
### Fixed
- Container image build.

## [0.5.0] - 2026-10-01
### Added
- `init`, `doctor`, `start`/`stop`/`status`/`logs`, `service install`; container image; docs.

## [0.4.0] - 2026-10-01
### Added
- `/diff` and `/undo` with git checkpoints, long replies as files, daily budgets, sandboxing.

## [0.3.0] - 2026-10-01
### Added
- ACP and command adapters, pickers, message queue, forum-topic sessions, voice replies.

## [0.2.0] - 2026-10-01
### Added
- First release: Telegram frontend, Claude Code adapter, voice transcription, approvals.

[Unreleased]: https://github.com/c0sm0thecoder/pocketagent/compare/v0.9.0...HEAD
[0.9.0]: https://github.com/c0sm0thecoder/pocketagent/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/c0sm0thecoder/pocketagent/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/c0sm0thecoder/pocketagent/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/c0sm0thecoder/pocketagent/compare/v0.5.1...v0.6.0
[0.5.1]: https://github.com/c0sm0thecoder/pocketagent/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/c0sm0thecoder/pocketagent/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/c0sm0thecoder/pocketagent/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/c0sm0thecoder/pocketagent/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/c0sm0thecoder/pocketagent/releases/tag/v0.2.0
