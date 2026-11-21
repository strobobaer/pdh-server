FROM golang:1.27-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG PDH_BUILD_COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.buildCommit=${PDH_BUILD_COMMIT}" -o /out/pdh ./cmd/server \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/update-agent ./cmd/update-agent \
    && mkdir -p /out/clients \
    && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.version=$(tr -d '[:space:]' < VERSION)" -o /out/clients/pdh-data-client-linux-amd64 ./cmd/pdh-data-client \
    && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.version=$(tr -d '[:space:]' < VERSION)" -o /out/clients/pdh-data-client-windows-amd64.exe ./cmd/pdh-data-client

FROM ubuntu:24.04 AS app
LABEL org.opencontainers.image.licenses="AGPL-3.0-or-later"

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
COPY --from=build --chown=10001:10001 /out/clients ./clients
# Lizenz (AGPL-3.0-or-later), Ausnahme für Ausgaben und Lizenzen der Fremdbestandteile
COPY LICENSE COPYING.md LICENSE-EXCEPTION.md THIRD_PARTY_LICENSES.md ./licenses/
COPY third_party/notices ./licenses/notices
ENV PDH_DATA_CLIENT_DIR=/app/clients

USER 10001:10001

EXPOSE 8090

HEALTHCHECK --interval=30s --timeout=5s --start-period=45s --retries=3 \
    CMD ["curl", "--fail", "--silent", "http://127.0.0.1:8090/health"]

ENTRYPOINT ["/app/pdh"]

FROM ubuntu:24.04 AS updater
LABEL org.opencontainers.image.licenses="AGPL-3.0-or-later"

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl git util-linux docker.io docker-compose-v2 cifs-utils nfs-common \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/update-agent /usr/local/bin/update-agent
COPY LICENSE COPYING.md LICENSE-EXCEPTION.md THIRD_PARTY_LICENSES.md /usr/share/licenses/pdh/
COPY third_party/notices /usr/share/licenses/pdh/notices
ENV PDH_UPDATE_REPO_DIR=/repo
EXPOSE 8091
ENTRYPOINT ["/usr/local/bin/update-agent"]

# code-server (VS Code im Browser) unter /code/ durch das PDH – mit Go, damit
# sich das PDH direkt im Browser bauen und testen lässt (compose.yaml: codeserver).
FROM codercom/code-server:4.96.4 AS codeserver
USER root
COPY --from=golang:1.27-bookworm /usr/local/go /usr/local/go
RUN apt-get update \
    && apt-get install -y --no-install-recommends make postgresql-client \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /data /workspace
ENV PATH=/usr/local/go/bin:/data/go/bin:$PATH \
    GOPATH=/data/go \
    GOTOOLCHAIN=auto
EXPOSE 8080
ENTRYPOINT ["/usr/bin/entrypoint.sh", "--auth", "none", "--bind-addr", "0.0.0.0:8080", "--disable-telemetry", "--disable-update-check", "--user-data-dir", "/data/user", "--extensions-dir", "/data/extensions", "--app-name", "PDH Code", "/workspace/pdh"]

FROM app AS final
