# pocketagent with agents preinstalled, for running on a server.
# See "Running on a server" in the README.

FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION}" -o /pocketagent ./cmd/pocketagent

FROM node:22-bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      git ca-certificates curl ripgrep python3 ffmpeg \
    && rm -rf /var/lib/apt/lists/*
RUN npm install -g @anthropic-ai/claude-code @agentclientprotocol/claude-agent-acp \
      @openai/codex @zed-industries/codex-acp @google/gemini-cli
COPY --from=build /pocketagent /usr/local/bin/pocketagent
# node images already have uid 1000 ("node"); rename it.
RUN usermod -l agent -d /home/agent -m node && groupmod -n agent node
USER agent
WORKDIR /home/agent
ENTRYPOINT ["pocketagent"]
CMD ["run"]
