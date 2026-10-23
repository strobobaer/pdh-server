FROM golang:1.24-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG PDH_BUILD_COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.buildCommit=${PDH_BUILD_COMMIT}" -o /out/pdh ./cmd/server \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/update-agent ./cmd/update-agent

FROM ubuntu:24.04 AS app

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system --gid 10001 pdh \
    && useradd --system --uid 10001 --gid pdh --home-dir /app --no-create-home pdh \
    && mkdir -p /app/uploads /app/chat_files /app/backups /app/config /app/logs \
    && chown -R 10001:10001 /app

WORKDIR /app

COPY --from=build --chown=10001:10001 /out/pdh ./pdh
COPY --chown=10001:10001 migrations ./migrations
COPY --chown=10001:10001 web ./web

USER 10001:10001

EXPOSE 8090

HEALTHCHECK --interval=30s --timeout=5s --start-period=45s --retries=3 \
    CMD ["curl", "--fail", "--silent", "http://127.0.0.1:8090/health"]

ENTRYPOINT ["/app/pdh"]

FROM ubuntu:24.04 AS updater

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl git util-linux docker.io docker-compose-v2 cifs-utils nfs-common \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/update-agent /usr/local/bin/update-agent
ENV PDH_UPDATE_REPO_DIR=/repo
EXPOSE 8091
ENTRYPOINT ["/usr/local/bin/update-agent"]

# code-server (VS Code im Browser) unter /code/ durch das PDH – mit Go, damit
# sich das PDH direkt im Browser bauen und testen lässt (compose.yaml: codeserver).
FROM codercom/code-server:4.96.4 AS codeserver
USER root
COPY --from=golang:1.24-bookworm /usr/local/go /usr/local/go
RUN apt-get update \
    && apt-get install -y --no-install-recommends make postgresql-client \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /data /workspace
ENV PATH=/usr/local/go/bin:/data/go/bin:$PATH \
    GOPATH=/data/go \
    GOTOOLCHAIN=auto
EXPOSE 8080
ENTRYPOINT ["/usr/bin/entrypoint.sh", "--auth", "none", "--bind-addr", "0.0.0.0:8080", "--disable-telemetry", "--disable-update-check", "--user-data-dir", "/data/user", "--extensions-dir", "/data/extensions", "--app-name", "PDH Code", "/workspace/pdh"]

# WireGuard-VPN ins Maschinennetz (compose.yaml: wireguard) – wendet die vom
# PDH erzeugte Konfiguration an (internal/web/vpn_conf.go). Braucht NET_ADMIN
# und WireGuard im Kernel des Hosts (Linux ab 5.6).
FROM alpine:3.20 AS wireguard
RUN apk add --no-cache bash coreutils iproute2 iptables wireguard-tools
COPY scripts/wireguard-sidecar.sh /usr/local/bin/wireguard-sidecar
RUN sed -i 's/\r$//' /usr/local/bin/wireguard-sidecar && chmod 755 /usr/local/bin/wireguard-sidecar
ENTRYPOINT ["/usr/local/bin/wireguard-sidecar"]

FROM app AS final
