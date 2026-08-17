GO ?= go
# One address for both the example server and the checks that drive it.
EXAMPLE_ADDR ?= :8321
TAILWIND_VERSION ?= v4.3.3
CHARTJS_VERSION ?= 4.5.0
LUCIDE_VERSION ?= 0.545.0
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

.PHONY: build test lint vet run e2e tidy assets assets-dev tailwind-bin vendor-chart vendor-lucide

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

# Re-vendors Chart.js. The result is committed, like Lucide's sprite, so a panel
# built from a released module has working charts — a consumer cannot run this
# Makefile, and an asset that only a clone can fetch never reaches them.
#
# It is served per page rather than bundled into app.js: Chart.js is larger than
# the whole UI bundle and most pages have no chart. Basecoat's own Chart
# component is small and is imported by app.js instead, because it attaches to
# window.basecoat and has to run after it.
#
# Run this after bumping CHARTJS_VERSION, then commit what it stages.
vendor-chart:
	@test -f frontend/vendor/chartjs/chart.umd.min.js || ( \
	  mkdir -p frontend/vendor/chartjs && \
	  echo "downloading chart.js $(CHARTJS_VERSION)..." && \
	  curl -sSL -o frontend/vendor/chartjs/chart.umd.min.js \
	    https://cdn.jsdelivr.net/npm/chart.js@$(CHARTJS_VERSION)/dist/chart.umd.min.js && \
	  curl -sSL -o frontend/vendor/chartjs/LICENSE.md \
	    https://raw.githubusercontent.com/chartjs/Chart.js/v$(CHARTJS_VERSION)/LICENSE.md )
	@mkdir -p assets/dist
	@cp frontend/vendor/chartjs/chart.umd.min.js assets/dist/chart.umd.min.js
	# MIT requires the notice to travel with the code, so it ships beside it.
	@cp frontend/vendor/chartjs/LICENSE.md assets/dist/chart.umd.min.LICENSE
	@echo "chart runtime staged in assets/dist — commit it"

# Vendors Lucide's full sprite (~400 KB, every icon as a <symbol>) into
# assets/dist, where go:embed ships it. It is the source for both the icon
# picker's grid and the server-side {{icon}} lookup, so one file covers every
# Lucide icon without bundling any of them into app.js.
#
# Committed rather than fetched per build, so a clone builds a working panel.
# Re-run after bumping LUCIDE_VERSION.
vendor-lucide:
	@mkdir -p assets/dist
	@echo "downloading lucide-static $(LUCIDE_VERSION) sprite..."
	@curl -sSfL -o assets/dist/lucide-sprite.svg \
	  https://cdn.jsdelivr.net/npm/lucide-static@$(LUCIDE_VERSION)/sprite.svg
	@curl -sSfL -o assets/dist/lucide-sprite.LICENSE \
	  https://cdn.jsdelivr.net/npm/lucide-static@$(LUCIDE_VERSION)/LICENSE
	@echo "sprite: $$(wc -c < assets/dist/lucide-sprite.svg) bytes, \
$$(grep -o '<symbol' assets/dist/lucide-sprite.svg | wc -l | tr -d ' ') icons"

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
	cd example && $(GO) run . -addr $(EXAMPLE_ADDR)

e2e:
	./scripts/e2e.sh

# Requires: npm i && npx playwright install webkit,
# plus a running example server (make run).
visual:
	node scripts/visual.mjs http://localhost$(EXAMPLE_ADDR)

tidy:
	$(GO) mod tidy
	cd example && $(GO) mod tidy
