# Sandboxing agents

By default an agent runs as you, on your machine, and can touch anything you can. Approvals keep you in the loop, but if you use `full` mode or "always allow" a lot, run the agent in a container instead.

Any agent can be wrapped: `wrap` is put in front of the agent's command, and `{cwd}` is the conversation's working directory.

## 1. Build the image

```sh
docker build -t pocketagent-sandbox sandbox/
# choose which agents to include:
docker build -t pocketagent-sandbox --build-arg AGENT_PACKAGES="@google/gemini-cli" sandbox/
```

## 2. Configure

```yaml
# Agents in containers reach pocketagent's tool server (send_file, approval
# hooks) through the Docker host. With this set, the server listens on all
# interfaces, still protected by a random per-run token.
bridge_host: host.docker.internal

agents:
  gemini-sandboxed:
    type: acp
    command: [gemini, --acp]
    wrap: [docker, run, --rm, -i,
           --add-host=host.docker.internal:host-gateway,
           -v, "{cwd}:{cwd}", -w, "{cwd}",
           -e, GEMINI_API_KEY,
           pocketagent-sandbox]
    env: ["GEMINI_API_KEY=${GEMINI_API_KEY}"]
```

The same `wrap` works for every agent type; change `command` and the credentials.

## Credentials

On macOS, agent logins are often kept in the Keychain, which a container can't read. Pass a token through the environment instead (`-e NAME` in `wrap`, the value in `env`). Each agent documents its own variable, for example:

| agent | variable |
|---|---|
| Claude Code | `ANTHROPIC_API_KEY`, or `CLAUDE_CODE_OAUTH_TOKEN` from `claude setup-token` |
| Codex | `OPENAI_API_KEY` |
| Gemini CLI | `GEMINI_API_KEY` |

## Notes

* `-i` is required: pocketagent talks to the agent over stdin and stdout.
* Only the project directory is mounted. `/undo` and `/diff` still work, because git checkpoints run on the host.
* Sessions live inside the container, so they're lost when it exits. To keep them, mount a volume for the agent's state directory.
* Network access is unrestricted. Add `--network none` for fully offline agents, though most agents need the network to reach their model.
