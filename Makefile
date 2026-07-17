.PHONY: run build tidy deploy push migrate migrate004 test

run:
	go run ./cmd/server/...

build:
	go build -o bin/pdh ./cmd/server/...

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
