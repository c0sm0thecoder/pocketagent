# A container image for running coding agents sandboxed under pocketagent.
# The agent only sees the project directory you mount; your home directory,
# SSH keys and other projects stay out of reach.
#
#   docker build -t pocketagent-sandbox sandbox/
#
# See docs/sandbox.md for the matching config.
FROM node:22-bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
      git ca-certificates curl ripgrep python3 build-essential \
    && rm -rf /var/lib/apt/lists/*

# Agents. Remove the ones you don't use.
RUN npm install -g @anthropic-ai/claude-code @agentclientprotocol/claude-agent-acp \
      @openai/codex @zed-industries/codex-acp @google/gemini-cli

# Run as an unprivileged user (Claude Code refuses bypassPermissions as root).
RUN useradd -m -u 1000 agent
USER agent
RUN git config --global user.name "pocketagent sandbox" && git config --global user.email "sandbox@localhost"
