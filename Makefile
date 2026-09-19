BINARY := dist/onthego
VERSION ?= 0.1.2-dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || printf unknown)
BUILT_AT ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.builtAt=$(BUILT_AT)

.PHONY: build receiver test check clean

build:
	mkdir -p dist
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/onthego

receiver:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/onthego-linux-amd64 ./cmd/onthego

test:
	go test ./...

check:
	gofmt -w cmd internal
	go vet ./...
	go test ./...

clean:
	rm -rf dist coverage.out
