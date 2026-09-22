GOLANGCI_LINT ?= go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2

.PHONY: build test lint fmt install

build:
	go build -trimpath -o bin/unreal ./cmd/unreal

test:
	go test -race ./...

lint:
	$(GOLANGCI_LINT) run ./...

fmt:
	$(GOLANGCI_LINT) fmt ./...

install: build
	mkdir -p $(HOME)/.local/bin
	ln -sf $(CURDIR)/bin/unreal $(HOME)/.local/bin/unreal
