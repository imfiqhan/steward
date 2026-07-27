package steward

import "embed"

// assetsFS holds the built frontend served under {prefix}/_assets/:
//
//   - dist/app.css — Tailwind v4 + Basecoat 1.0.2, compiled by the Tailwind
//     standalone binary (no Node at runtime or build; see tools/assets)
//   - dist/app.js — htmx 2.0.10 + Basecoat JS + Steward glue, bundled by
//     the esbuild Go API
//   - icons/*.svg — Lucide 0.545.0 subset, inlined by the {{icon}} func
//
// Rebuild with `make assets`; sources live in frontend/.
//
//go:embed all:assets
var assetsFS embed.FS

var _ = assetsFS // consumed by the renderer; keeps the embed compiled under no_ui
