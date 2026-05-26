.PHONY: run build tidy fmt test check deploy push

run:
	go run ./cmd/server/...

build:
	go build -o bin/pdh ./cmd/server/...

tidy:
	go mod tidy

fmt:
	gofmt -w .

test:
	go test ./...

check:
	@files=$$(gofmt -l .); \
	if [ -n "$$files" ]; then \
		echo "Die folgenden Dateien sind nicht gofmt-formatiert:"; \
		echo "$$files"; \
		exit 1; \
	fi
	go test ./...
	go build ./cmd/server/...

deploy: check build
	sudo systemctl restart pdh
	@sleep 2
	@sudo systemctl status pdh --no-pager -l | head -8

push:
	git push origin main
	git push gitea main
	@echo "GitHub + Gitea aktualisiert"
