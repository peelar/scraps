INSTALL_DIR ?= $(HOME)/.local/bin
OPENSHELL_VERSION ?= v0.0.113

.PHONY: build build-go check clean dev-daemon docker-image down install openshell-ready sync-extension test uninstall up

# One-command local setup: install/start OpenShell, build the workspace image,
# install stable PATH entries, and ensure scrapd.
up: openshell-ready docker-image install
	$(INSTALL_DIR)/scrap up

openshell-ready:
	@if [ -n "$(SCRAPD_PROVIDER)" ] && [ "$(SCRAPD_PROVIDER)" != "openshell" ]; then \
		echo "OpenShell bootstrap skipped for $(SCRAPD_PROVIDER) provider"; \
		exit 0; \
	fi; \
	expected="$(OPENSHELL_VERSION)"; expected="$${expected#v}"; \
	installed="$$(openshell --version 2>/dev/null | awk 'NR == 1 { print $$2 }')"; \
	if [ "$$installed" != "$$expected" ]; then \
		echo "installing OpenShell $(OPENSHELL_VERSION) (found $${installed:-none})..."; \
		curl -LsSf "https://raw.githubusercontent.com/NVIDIA/OpenShell/$(OPENSHELL_VERSION)/install.sh" | \
			OPENSHELL_VERSION="$(OPENSHELL_VERSION)" sh; \
	fi; \
	openshell status >/dev/null || { \
		echo "OpenShell is installed but its gateway is not ready; inspect its service logs" >&2; \
		exit 1; \
	}; \
	echo "OpenShell gateway ready → $$(openshell --version | head -1)"

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
