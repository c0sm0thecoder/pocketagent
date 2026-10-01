# Sandboxing agents

By default an agent runs as you, on your machine, and can touch anything you can. Approvals keep you in the loop, but if you use `yolo` mode or "always allow" a lot, run the agent in a container instead.

Any agent can be wrapped: `wrap` is put in front of the agent's command, and `{cwd}` is the conversation's working directory.

## 1. Build the image

```sh
docker build -t pocketagent-sandbox sandbox/
```

## 2. Configure

```yaml
# Agents in containers reach pocketagent's MCP server (approvals, send_file)
# through the Docker host. With this set, the server listens on all
# interfaces, still protected by a random per-run token.
bridge_host: host.docker.internal

agents:
  claude-sandboxed:
    type: claude
    command: claude
    wrap: [docker, run, --rm, -i,
           --add-host=host.docker.internal:host-gateway,
           -v, "{cwd}:{cwd}", -w, "{cwd}",
           -e, CLAUDE_CODE_OAUTH_TOKEN,
           pocketagent-sandbox]
    env: ["CLAUDE_CODE_OAUTH_TOKEN=${CLAUDE_CODE_OAUTH_TOKEN}"]

  codex-sandboxed:
    type: acp
    command: [codex-acp]
    wrap: [docker, run, --rm, -i,
           --add-host=host.docker.internal:host-gateway,
           -v, "{cwd}:{cwd}", -w, "{cwd}",
           -e, OPENAI_API_KEY,
           pocketagent-sandbox]
```

## Credentials

On macOS, agent logins live in the Keychain, which a container can't read. Pass a token instead:

* Claude Code: `claude setup-token` prints a long-lived token. Export it as `CLAUDE_CODE_OAUTH_TOKEN`, or use `ANTHROPIC_API_KEY`.
* Codex: `OPENAI_API_KEY`.
* Gemini: `GEMINI_API_KEY`.

## Notes

* `-i` is required: pocketagent talks to the agent over stdin and stdout.
* Only the project directory is mounted. `/undo` and `/diff` still work, because git checkpoints run on the host.
* Sessions live inside the container, so they're lost when it exits. To keep them, mount a volume for the agent's state (for example `-v pocketagent-claude:/home/agent/.claude`).
* Network access is unrestricted. Add `--network none` for fully offline agents, though most agents need the network to reach their model.
