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
	git push origin main --force
	git push gitea main --force
	@echo "✅ GitHub + Gitea aktualisiert"
