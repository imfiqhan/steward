GO ?= go

.PHONY: build test lint vet run e2e tidy

build:
	$(GO) build ./...
	cd example && $(GO) build ./...

test:
	$(GO) test -race ./...
	cd example && $(GO) test -race ./...

vet:
	$(GO) vet ./...
	cd example && $(GO) vet ./...

lint:
	golangci-lint run ./...
	cd example && golangci-lint run ./...

run:
	cd example && $(GO) run . serve

e2e:
	./scripts/e2e.sh

tidy:
	$(GO) mod tidy
	cd example && $(GO) mod tidy
