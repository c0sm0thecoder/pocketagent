# Security

pocketagent gives a Telegram chat control over a coding agent that runs on your machine. Treat it like SSH access.

## The trust model

- **Who can use it:** only the Telegram user ids in `telegram.allowed_users`. The bot refuses to start without at least one, ignores messages and button taps from anyone else, and logs their ids.
- **What the agent can do:** whatever the agent's own permission system plus pocketagent's mode allows, as your OS user. In `ask` mode, anything other than `auto_allow` kinds (read, search and think by default) needs a tap. Your existing agent settings still apply, so tools you pre-approved there won't ask.
- **The bot token:** anyone with it can impersonate your bot, but they can't get past the allowlist, because Telegram signs user ids. Still, revoke a leaked token with @BotFather (`/revoke`).
- **The MCP bridge:** agents reach pocketagent through an HTTP server on 127.0.0.1, protected by a random token generated at each start. With `bridge_host` set (for containers) it listens on all interfaces, still behind the token.
- **Files:** uploads and state live in `~/.pocketagent` with mode 600/700.

## Hardening

- Keep `mode: ask` as the default and use `edits` or `full` per project.
- Remove `read` from `auto_allow` if agents shouldn't read files outside the project without asking.
- Run agents in a container (`wrap`, see docs/sandbox.md) so they only see the project directory.
- Set `budget.daily_usd`.
- Use a dedicated bot per machine, and don't add the bot to groups with people who aren't on the allowlist. Non-allowlisted members can't control it, but they can read its replies.

## Reporting a vulnerability

Please report security issues privately through GitHub's "Report a vulnerability" (Security tab), not in public issues.
