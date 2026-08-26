INSTALL_DIR ?= $(HOME)/.local/bin

.PHONY: build build-go check clean dev-daemon docker-image down install sync-extension test uninstall up

# One-command local setup: build, install stable PATH entries, ensure daemon.
up: docker-image install
	$(INSTALL_DIR)/scrap up

install: build
	mkdir -p $(INSTALL_DIR)
	ln -sfn $(CURDIR)/bin/scrap $(INSTALL_DIR)/scrap
	ln -sfn $(CURDIR)/bin/scrapd $(INSTALL_DIR)/scrapd
	@echo "installed scrap + scrapd → $(INSTALL_DIR)"

uninstall:
	rm -f $(INSTALL_DIR)/scrap $(INSTALL_DIR)/scrapd

down:
	$(INSTALL_DIR)/scrap down

build: sync-extension build-go

sync-extension:
	rsync -a --delete --exclude '*.test.ts' packages/pi-extension/src/ internal/extension/files/

build-go:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o bin/scrap ./cmd/scrap
	CGO_ENABLED=0 go build -trimpath -o bin/scrapd ./cmd/scrapd

check:
	test -z "$$(gofmt -l cmd internal)"
	go test ./...
	go vet ./...
	pnpm check

clean:
	rm -rf bin

dev-daemon:
	go run ./cmd/scrapd

docker-image:
	docker build --pull -t scraps-dev:bookworm docker

test:
	go test ./...
	pnpm test
