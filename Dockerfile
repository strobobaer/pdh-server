# syntax=docker/dockerfile:1

FROM golang:1.22.2-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/pdh ./cmd/server/...

FROM debian:bookworm-slim
WORKDIR /app

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/pdh /app/pdh
COPY web /app/web
COPY migrations /app/migrations
COPY .env.example /app/.env.example

RUN useradd --system --home /app --shell /usr/sbin/nologin pdh \
    && mkdir -p /app/uploads \
    && chown -R pdh:pdh /app

USER pdh
EXPOSE 8090

ENTRYPOINT ["/app/pdh"]
