# Changelog

All notable changes are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/). Until 1.0, minor versions may change the config format; such changes are called out with migration notes.

## [Unreleased]

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

[Unreleased]: https://github.com/c0sm0thecoder/pocketagent/compare/v0.7.0...HEAD
[0.7.0]: https://github.com/c0sm0thecoder/pocketagent/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/c0sm0thecoder/pocketagent/compare/v0.5.1...v0.6.0
[0.5.1]: https://github.com/c0sm0thecoder/pocketagent/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/c0sm0thecoder/pocketagent/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/c0sm0thecoder/pocketagent/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/c0sm0thecoder/pocketagent/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/c0sm0thecoder/pocketagent/releases/tag/v0.2.0
