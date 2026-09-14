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

.PHONY: build test lint vet noui run e2e tidy assets assets-dev tailwind-bin vendor-chart vendor-lucide

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
	  	  curl -sSfL --retry 3 -o frontend/.bin/tailwindcss \
	    https://github.com/tailwindlabs/tailwindcss/releases/download/$(TAILWIND_VERSION)/tailwindcss-$(TW_OS)-$(TW_ARCH) && \
	  chmod +x frontend/.bin/tailwindcss && \
	  frontend/.bin/tailwindcss --help >/dev/null )

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
	  	  curl -sSfL --retry 3 -o frontend/vendor/chartjs/chart.umd.min.js \
	    https://cdn.jsdelivr.net/npm/chart.js@$(CHARTJS_VERSION)/dist/chart.umd.min.js && \
	  	  curl -sSfL --retry 3 -o frontend/vendor/chartjs/LICENSE.md \
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

# Every target walks all three modules. The framework, its example and the
# scaffolder are separate modules, so `./...` in any one of them covers one of
# them — which is how the scaffolder went unvetted and untested entirely.
MODULES = . example cmd/steward

# Linted and built, not tested: they have no tests of their own, and they are
# separately versioned, so they are not part of the test gate. CI lints them,
# and a gate narrower than CI's is a gate that reports green on a red push.
CONTRIB = contrib/ginsteward contrib/redcache contrib/meilistore contrib/s3store

build:
	@for dir in $(MODULES) $(CONTRIB); do echo "== $$dir"; (cd $$dir && $(GO) build ./...) || exit 1; done
		@echo "== each module on its own, the way a consumer resolves it"
	@for dir in $(MODULES) $(CONTRIB); do echo "== $$dir (GOWORK=off)"; (cd $$dir && GOWORK=off $(GO) build ./...) || exit 1; done

test:
	@for dir in $(MODULES); do echo "== $$dir"; (cd $$dir && $(GO) test -race ./...) || exit 1; done

vet:
	@for dir in $(MODULES); do echo "== $$dir"; (cd $$dir && $(GO) vet ./...) || exit 1; done

lint:
	@for dir in $(MODULES) $(CONTRIB) tools/assets; do echo "== $$dir"; (cd $$dir && golangci-lint run ./...) || exit 1; done

# The no_ui tag compiles the templates and assets out. It is a separate
# compilation of the whole package, so nothing short of building it catches a
# reference to something only the UI half provides — which is how it sat broken
# through several releases.
#
# The example's own suite is not run under the tag: most of it asserts HTML,
# which is the thing this build does not produce. The tests written for it are.
noui:
	$(GO) build -tags no_ui ./...
	$(GO) vet -tags no_ui ./...
	$(GO) test -tags no_ui ./...
	cd example && $(GO) build -tags no_ui ./...
	cd example && $(GO) vet -tags no_ui ./...
	cd example && $(GO) test -tags no_ui -count=1 -run TestNoUI ./

run:
	cd example && $(GO) run . -addr $(EXAMPLE_ADDR)

e2e:
	./scripts/e2e.sh

# Requires: npm i && npx playwright install webkit,
# plus a running example server (make run).
visual:
	node scripts/visual.mjs http://localhost$(EXAMPLE_ADDR)

tidy:
	@for dir in $(MODULES); do echo "== $$dir"; (cd $$dir && $(GO) mod tidy) || exit 1; done
