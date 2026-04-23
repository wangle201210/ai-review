FROM node:20-slim
RUN apt-get update && apt-get install -y --no-install-recommends git curl jq ca-certificates \
    && update-ca-certificates \
    && rm -rf /var/lib/apt/lists/*
RUN ARCH=$(uname -m) && \
    if [ "$ARCH" = "aarch64" ]; then \
      NATIVE_PKG="@anthropic-ai/claude-code-linux-arm64@2.1.118"; \
    else \
      NATIVE_PKG="@anthropic-ai/claude-code-linux-x64@2.1.118"; \
    fi && \
    npm install -g @anthropic-ai/claude-code@2.1.118 $NATIVE_PKG
COPY ai-review-linux-amd64 /usr/local/bin/ai-review
RUN chmod +x /usr/local/bin/ai-review
WORKDIR /app
ENTRYPOINT ["ai-review"]
