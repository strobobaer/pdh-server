.PHONY: run build build-update-agent build-data-client tidy deploy push migrate migrate004 test

run:
	go run ./cmd/server/...

build:
	mkdir -p bin
	go build -trimpath -ldflags="-X main.buildCommit=$(shell git rev-parse HEAD)" -o bin/pdh ./cmd/server
	go build -trimpath -o bin/update-agent ./cmd/update-agent
	$(MAKE) build-data-client

build-update-agent:
	go build -trimpath -o bin/update-agent ./cmd/update-agent

build-data-client:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.version=$(shell tr -d '[:space:]' < VERSION)" -o bin/pdh-data-client-linux-amd64 ./cmd/pdh-data-client
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.version=$(shell tr -d '[:space:]' < VERSION)" -o bin/pdh-data-client-windows-amd64.exe ./cmd/pdh-data-client

tidy:
	go mod tidy

deploy: build
	sudo systemctl restart pdh
	@sleep 2
	@sudo systemctl status pdh --no-pager -l | head -8

push:
	git push origin main
	git push gitea main
	@echo "✅ GitHub + Gitea aktualisiert"

migrate:
	psql -U $$PDH_DATABASE_USER -d $$PDH_DATABASE_NAME \
	  -h $$PDH_DATABASE_HOST -W -f migrations/003_shifts_schema.up.sql

migrate004:
	psql -U $$PDH_DATABASE_USER -d $$PDH_DATABASE_NAME \
	  -h $$PDH_DATABASE_HOST -W -f migrations/004_fix_schema_gaps.up.sql

test:
	go test ./...
