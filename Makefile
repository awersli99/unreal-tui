.PHONY: build test install

build:
	go build -trimpath -o bin/unreal .

test:
	go test -race ./...

install: build
	mkdir -p $(HOME)/.local/bin
	ln -sf $(CURDIR)/bin/unreal $(HOME)/.local/bin/unreal
