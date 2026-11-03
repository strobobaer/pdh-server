# pdh-ha als Container: Zeuge (Standardziel) und Knoten im Testlabor (Ziel lab).
# Build-Kontext ist die Repo-Wurzel:
#   docker build -f deploy/ha-witness/Dockerfile --build-arg COMMIT=$(git rev-parse HEAD) .
FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY internal/ha ./internal/ha
COPY cmd/pdh-ha ./cmd/pdh-ha
COPY VERSION ./
ARG VERSION=""
ARG COMMIT=""
RUN v="${VERSION:-$(tr -d '[:space:]' < VERSION)}" \
    && CGO_ENABLED=0 go build -buildvcs=false -trimpath \
       -ldflags "-s -w -X main.version=${v} -X main.commit=${COMMIT}" \
       -o /out/pdh-ha ./cmd/pdh-ha

FROM debian:bookworm-slim AS base
RUN apt-get update \
    && apt-get install -y --no-install-recommends iputils-ping \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/pdh-ha /usr/local/bin/pdh-ha
COPY deploy/ha-witness/entrypoint.sh /usr/local/bin/pdh-ha-entrypoint
# Windows-Checkout (CRLF) darf den Start nicht verhindern
RUN sed -i 's/\r$//' /usr/local/bin/pdh-ha-entrypoint \
    && chmod 0755 /usr/local/bin/pdh-ha-entrypoint \
    && install -d -m 0700 /etc/pdh/ha \
    && install -d -m 0700 /var/lib/pdh-ha
VOLUME ["/etc/pdh/ha", "/var/lib/pdh-ha"]
EXPOSE 7480/tcp
ENTRYPOINT ["/usr/local/bin/pdh-ha-entrypoint"]

# Testlabor: zusätzlich curl für Handbefehle über den Socket (Rollentausch)
FROM base AS lab
RUN apt-get update \
    && apt-get install -y --no-install-recommends curl \
    && rm -rf /var/lib/apt/lists/*

# Testlabor mit PostgreSQL (Ubuntu wie in der Produktion); pdh als Platzhalter.
# iproute2 und iputils-arping für die mitwandernde Adresse (wie in der
# Produktion; nicht das Paket arping – das hat andere Schalter)
FROM ubuntu:26.04 AS lab-pg
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
       postgresql postgresql-contrib iputils-ping iputils-arping iproute2 curl ca-certificates socat \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/pdh-ha /usr/local/bin/pdh-ha
COPY deploy/ha-lab/pdh-dummy.sh /usr/local/bin/pdh-dummy
COPY deploy/ha-lab/lab-agent.sh /usr/local/bin/lab-agent
COPY deploy/ha-lab/lab-pg-entrypoint.sh /usr/local/bin/pdh-ha-entrypoint
COPY VERSION /usr/local/share/pdh-lab/VERSION
RUN sed -i 's/\r$//' /usr/local/bin/pdh-dummy /usr/local/bin/lab-agent /usr/local/bin/pdh-ha-entrypoint \
    && chmod 0755 /usr/local/bin/pdh-dummy /usr/local/bin/lab-agent /usr/local/bin/pdh-ha-entrypoint \
    && install -d -m 0700 /etc/pdh/ha /var/lib/pdh-ha \
    && install -d -m 0755 /var/lib/pdh-lab
# Update im Verbund: Agent und pdh-Platzhalter (lab-agent, pdh-dummy), Stand
# je Takt aus der Datei, /health auf 8090
ENV PDH_HA_SERVICE_CMD=/usr/local/bin/pdh-dummy \
    PDH_HA_AGENT_CMD=/usr/local/bin/lab-agent \
    PDH_HA_COMMIT_FILE=/var/lib/pdh-lab/commit \
    PDH_SERVER_PORT=8090
EXPOSE 7480/tcp 5432/tcp
ENTRYPOINT ["/usr/local/bin/pdh-ha-entrypoint"]

# Standardziel (zuletzt): Zeuge
FROM base AS witness
