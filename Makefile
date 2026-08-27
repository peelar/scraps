INSTALL_DIR ?= $(HOME)/.local/bin
.PHONY: build build-go check clean configure configure-remote-client deploy-worker down homelab-acceptance homelab-smoke install sync-extension test uninstall up vm-delete vm-down vm-shell vm-status vm-up worker-bundles worker-check

# Default deployment: OpenShell, its runtime, scrapd, workspace data, and the
# durable headless Pi runner live inside one ordinary Linux worker VM. The Pi
# TUI remains on this host as a reconnectable client.
up: install
	./scripts/worker-vm up

configure: install
	$(INSTALL_DIR)/scrap configure

deploy-worker:
	@test -n "$(REMOTE)" || { echo "usage: make deploy-worker REMOTE=user@worker-host" >&2; exit 2; }
	./scripts/deploy-worker "$(REMOTE)"

configure-remote-client: build
	@test -n "$(REMOTE)" || { echo "usage: make configure-remote-client REMOTE=user@worker-host" >&2; exit 2; }
	./scripts/configure-remote-client "$(REMOTE)"

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
	@if [ -f $$(${XDG_CONFIG_HOME:-$$HOME/.config})/scraps/client.json ]; then \
		echo "client profile found — ready; check it with: scrap status"; \
	elif $(INSTALL_DIR)/scrap attach; then \
		echo "worker attached — check it with: scrap status, then in Pi: /scrap"; \
	else \
		echo "no worker attached (see above); later: scrap attach, or start a local one: make up"; \
	fi

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
	$(MAKE) worker-check

worker-check:
	bash -n scripts/worker-vm scripts/build-worker-bundle scripts/deploy-worker scripts/configure-remote-client scripts/homelab-smoke scripts/worker-acceptance deploy/worker/install deploy/worker/scraps-worker
	./scripts/test-worker-deployment

worker-bundles:
	./scripts/build-worker-bundle amd64
	./scripts/build-worker-bundle arm64

homelab-smoke:
	./scripts/homelab-smoke

homelab-acceptance:
	./scripts/worker-acceptance

clean:
	rm -rf bin

test:
	go test ./...
	pnpm test
