FROM node:22-alpine AS ui-builder
WORKDIR /src
RUN npm install --global pnpm@11.18.0
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY ui/package.json ui/package.json
COPY packages/floatlab-client/package.json packages/floatlab-client/package.json
RUN pnpm install --frozen-lockfile --filter floatlab-ui...
COPY ui/ ui/
COPY packages/floatlab-client/ packages/floatlab-client/
RUN pnpm --dir ui build

FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /floatlab-control ./cmd/floatlab-control

FROM debian:12-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates wget && rm -rf /var/lib/apt/lists/*
COPY --from=builder /floatlab-control /usr/local/bin/floatlab-control
WORKDIR /usr/share/floatlab
COPY --from=ui-builder /src/ui/dist ./ui/dist
USER nobody
ENTRYPOINT ["floatlab-control"]
