FROM node:22-bookworm-slim AS node
FROM docker:cli AS docker
FROM golang:1.25-bookworm

COPY --from=node /usr/local/ /usr/local/
COPY --from=docker /usr/local/bin/docker /usr/local/bin/docker
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl git gh openssh-client tini ripgrep python3 \
    && rm -rf /var/lib/apt/lists/*
ARG CODEX_VERSION=latest
ARG CLAUDE_VERSION=latest
ARG CODE_SERVER_VERSION=4.105.1
RUN npm install --global pnpm@11.18.0 @openai/codex@${CODEX_VERSION} @anthropic-ai/claude-code@${CLAUDE_VERSION}
RUN curl -fsSL https://code-server.dev/install.sh -o /tmp/install-code-server.sh \
    && sh /tmp/install-code-server.sh --version "$CODE_SERVER_VERSION" \
    && rm /tmp/install-code-server.sh
RUN GOBIN=/usr/local/bin go install github.com/air-verse/air@v1.61.7

WORKDIR /opt/floatlab-source
ENV pnpm_config_store_dir=/opt/pnpm/store
COPY . .
# Give the baked source a real upstream parent, so appliance changes can become PRs.
ARG SOURCE_REPOSITORY=https://github.com/floatlab-hq/floatlab-core.git
ARG SOURCE_REF=main
RUN git init && git remote add origin "$SOURCE_REPOSITORY" \
    && git fetch origin "$SOURCE_REF" && git reset --mixed FETCH_HEAD \
    && go mod download \
    && pnpm install --frozen-lockfile
COPY docker/dev/entrypoint.sh /usr/local/bin/floatlab-dev
COPY docker/dev/hostd.sh /usr/local/bin/floatlab-dev-hostd
COPY docker/dev/pr.sh /usr/local/bin/floatlab-dev-pr
RUN chmod +x /usr/local/bin/floatlab-dev*
ENV HOME=/root VITE_API_PROXY_TARGET=http://127.0.0.1:8081
WORKDIR /workspace
ENTRYPOINT ["tini", "--", "floatlab-dev"]
