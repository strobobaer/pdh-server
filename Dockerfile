FROM golang:1.22.2-bookworm AS build

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
    && apt-get install -y --no-install-recommends ca-certificates curl git util-linux docker.io docker-compose-v2 \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/update-agent /usr/local/bin/update-agent
ENV PDH_UPDATE_REPO_DIR=/repo
EXPOSE 8091
ENTRYPOINT ["/usr/local/bin/update-agent"]

FROM app AS final
