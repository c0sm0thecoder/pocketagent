# A container image for running coding agents sandboxed under pocketagent.
# The agent only sees the project directory you mount; your home directory,
# SSH keys and other projects stay out of reach.
#
#   docker build -t pocketagent-sandbox sandbox/
#
# See docs/sandbox.md for the matching config.
FROM node:24-bookworm-slim@sha256:0e0ff40c39bc087845bfb27465a0df4ea419520094bc35842ff83dd8cbe6f9b6

RUN apt-get update && apt-get install -y --no-install-recommends \
      git ca-certificates curl ripgrep python3 build-essential \
    && rm -rf /var/lib/apt/lists/*

# Agents to preinstall; override with --build-arg AGENT_PACKAGES="...".
ARG AGENT_PACKAGES="@agentclientprotocol/claude-agent-acp @anthropic-ai/claude-code @google/gemini-cli @openai/codex @zed-industries/codex-acp"
RUN npm install -g $AGENT_PACKAGES

# Run as an unprivileged user; some agents refuse full-access modes as root.
# node images already have uid 1000 ("node"); rename it.
RUN usermod -l agent -d /home/agent -m node && groupmod -n agent node
USER 1000
RUN git config --global user.name "pocketagent sandbox" && git config --global user.email "sandbox@localhost"
