# Spiegel-Labor (nur Testumgebung) – gebaut aus dem Repo-Wurzelordner
# (compose.yaml: context ../..).
#   src: Quellen + Modul-Cache, PDH-Server und fakeha gebaut; darin läuft der
#        Crawler (go test -tags mirrorlab).
#   app: PDH wie in der Produktion (migrations/ und web/ neben dem Programm),
#        unter einer Neustart-Schleife statt systemd.
FROM golang:1.24-bookworm AS src
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-X main.buildCommit=mirror-lab" -o /out/pdh ./cmd/server \
    && CGO_ENABLED=0 go build -trimpath -o /out/fakeha ./deploy/mirror-lab/fakeha \
    && CGO_ENABLED=0 go test -tags mirrorlab -count=1 -run '^$' ./internal/web >/dev/null

FROM ubuntu:24.04 AS app
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=src /out/pdh /out/fakeha ./
COPY migrations ./migrations
COPY web ./web
COPY deploy/mirror-lab/pdh-loop.sh /usr/local/bin/pdh-loop
EXPOSE 8090
CMD ["bash", "/usr/local/bin/pdh-loop"]
