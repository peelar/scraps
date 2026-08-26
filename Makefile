INSTALL_DIR ?= $(HOME)/.local/bin
.PHONY: build build-go check clean configure down install sync-extension test uninstall up vm-delete vm-down vm-shell vm-status vm-up

# Default deployment: OpenShell, its runtime, scrapd, and workspace data live
# inside one ordinary Linux worker VM. Pi and the client remain on this host.
up: install
	./scripts/worker-vm up

configure: install
	$(INSTALL_DIR)/scrap configure

vm-up: up

vm-down:
	./scripts/worker-vm stop

vm-delete:
	./scripts/worker-vm delete

vm-status:
	./scripts/worker-vm status

vm-shell:
	./scripts/worker-vm shell

install: build
	mkdir -p $(INSTALL_DIR) $(HOME)/.pi/agent/extensions
	ln -sfn $(CURDIR)/bin/scrap $(INSTALL_DIR)/scrap
	ln -sfn $(CURDIR)/packages/pi-extension/src $(HOME)/.pi/agent/extensions/scraps
	@echo "installed scrap → $(INSTALL_DIR)"
	@echo "installed Pi /scrap command → $(HOME)/.pi/agent/extensions/scraps"

uninstall:
	rm -f $(INSTALL_DIR)/scrap $(HOME)/.pi/agent/extensions/scraps

down: vm-down

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

test:
	go test ./...
	pnpm test
