FROM golang:1.22-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# hostd is CGo-free; it shells out to zfs/zpool/docker installed on the host.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /floatlab-hostd ./cmd/floatlab-hostd

FROM debian:12-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=builder /floatlab-hostd /usr/local/bin/floatlab-hostd
# hostd must run as root to manage ZFS and Docker.
ENTRYPOINT ["floatlab-hostd"]
