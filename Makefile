.PHONY: run build tidy deploy migrate test

run:
	go run ./cmd/server/...

build:
	go build -o bin/pdh ./cmd/server/...

tidy:
	go mod tidy

deploy: build
	sudo systemctl restart pdh
	@echo "✅ PDH deployed"
	@sudo systemctl status pdh --no-pager -l | head -10

migrate:
	psql -U $$PDH_DATABASE_USER -d $$PDH_DATABASE_NAME \
	  -h $$PDH_DATABASE_HOST -W \
	  -f migrations/002_faults_schema.up.sql

test:
	go test ./...
