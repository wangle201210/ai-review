FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /ai-review ./cmd/ai-review

FROM node:20-slim
ARG CLAUDE_CODE_VERSION=latest
ARG CODEX_CLI_VERSION=0.145.0
RUN apt-get update && apt-get install -y --no-install-recommends git curl jq ca-certificates \
    && update-ca-certificates \
    && rm -rf /var/lib/apt/lists/*
RUN ARCH=$(uname -m) && \
    if [ "$ARCH" = "aarch64" ]; then \
      NATIVE_PKG="@anthropic-ai/claude-code-linux-arm64@${CLAUDE_CODE_VERSION}"; \
    else \
      NATIVE_PKG="@anthropic-ai/claude-code-linux-x64@${CLAUDE_CODE_VERSION}"; \
    fi && \
    npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION} $NATIVE_PKG \
      @openai/codex@${CODEX_CLI_VERSION}
COPY --from=builder /ai-review /usr/local/bin/ai-review
COPY --from=builder /app/conf /app/conf
WORKDIR /app
ENTRYPOINT ["ai-review"]
