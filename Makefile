GO ?= go
TAILWIND_VERSION ?= v4.3.3
UNAME_S := $(shell uname -s | tr A-Z a-z)
UNAME_M := $(shell uname -m)
ifeq ($(UNAME_M),x86_64)
  TW_ARCH := x64
else
  TW_ARCH := arm64
endif
ifeq ($(UNAME_S),darwin)
  TW_OS := macos
else
  TW_OS := linux
endif

.PHONY: build test lint vet run e2e tidy assets assets-dev tailwind-bin

# One-shot frontend build (esbuild via Go + Tailwind standalone — no Node).
assets: tailwind-bin
	$(GO) run ./tools/assets

# Rebuild on change during development.
assets-dev: tailwind-bin
	$(GO) run ./tools/assets -watch

# Installs the Tailwind standalone binary (native, no Node) once.
tailwind-bin:
	@test -x frontend/.bin/tailwindcss || ( \
	  mkdir -p frontend/.bin && \
	  echo "downloading tailwindcss $(TAILWIND_VERSION) ($(TW_OS)-$(TW_ARCH))..." && \
	  curl -sSL -o frontend/.bin/tailwindcss \
	    https://github.com/tailwindlabs/tailwindcss/releases/download/$(TAILWIND_VERSION)/tailwindcss-$(TW_OS)-$(TW_ARCH) && \
	  chmod +x frontend/.bin/tailwindcss )

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

# Requires: npm i playwright && npx playwright install webkit,
# plus a running example server (make run).
visual:
	node scripts/visual.mjs

tidy:
	$(GO) mod tidy
	cd example && $(GO) mod tidy
