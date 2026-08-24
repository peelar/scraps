.PHONY: build build-go check clean dev-daemon test

build: build-go

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
