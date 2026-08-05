GO ?= go
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

# Vendors Chart.js and stages the chart runtime beside the UI bundle. Chart.js
# is Basecoat's Chart peer dependency and is not redistributed here, so this
# fetches it once. Charts are served per page, not bundled into app.js, because
# Chart.js is larger than the whole current bundle and most pages have no chart.
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
	@cp frontend/vendor/basecoat/js/chart.min.js assets/dist/basecoat-chart.min.js
	@echo "chart runtime staged in assets/dist (rebuild the binary to embed it)"

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
