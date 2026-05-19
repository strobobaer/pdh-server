.PHONY: run build tidy deploy push

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
	@echo "GitHub + Gitea aktualisiert"
