# pocketagent with agents preinstalled, for running on a server.
# See "Running on a server" in the README.

FROM golang:1.26@sha256:0f063af2d465d8dcae54cce04278ada488b96f77b42449c8d071e47d016cc65a AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION}" -o /pocketagent ./cmd/pocketagent

FROM node:24-bookworm-slim@sha256:0e0ff40c39bc087845bfb27465a0df4ea419520094bc35842ff83dd8cbe6f9b6
RUN apt-get update && apt-get install -y --no-install-recommends \
      git ca-certificates curl ripgrep python3 ffmpeg \
    && rm -rf /var/lib/apt/lists/*
# Agents to preinstall; override with --build-arg AGENT_PACKAGES="...".
ARG AGENT_PACKAGES="@agentclientprotocol/claude-agent-acp @anthropic-ai/claude-code @google/gemini-cli @openai/codex @zed-industries/codex-acp"
RUN npm install -g $AGENT_PACKAGES
COPY --from=build /pocketagent /usr/local/bin/pocketagent
# node images already have uid 1000 ("node"); rename it.
RUN usermod -l agent -d /home/agent -m node && groupmod -n agent node
USER 1000
WORKDIR /home/agent
ENTRYPOINT ["pocketagent"]
CMD ["run"]
