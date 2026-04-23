FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /ai-review ./cmd/ai-review

FROM node:20-slim
RUN apt-get update && apt-get install -y --no-install-recommends git curl jq \
    && rm -rf /var/lib/apt/lists/*
RUN ARCH=$(uname -m) && \
    if [ "$ARCH" = "aarch64" ]; then \
      NATIVE_PKG="@anthropic-ai/claude-code-linux-arm64@2.1.118"; \
    else \
      NATIVE_PKG="@anthropic-ai/claude-code-linux-x64@2.1.118"; \
    fi && \
    npm install -g @anthropic-ai/claude-code@2.1.118 $NATIVE_PKG
COPY --from=builder /app/ai-review /usr/local/bin/ai-review
WORKDIR /app
ENTRYPOINT ["ai-review"]
