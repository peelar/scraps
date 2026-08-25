.PHONY: build build-go check clean dev-daemon sync-extension test

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

test:
	go test ./...
	pnpm test
