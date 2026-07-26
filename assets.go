package steward

import "embed"

// assetsFS holds the vendored frontend assets served under {prefix}/_assets/.
//
// Pinned versions — update deliberately, they are part of the public UI contract:
//   - Tabler core 1.4.0 (css/tabler.min.css, js/tabler.min.js — bundles Bootstrap 5.3.7)
//   - htmx 2.0.10 (js/htmx.min.js)
//   - Alpine.js 3.15.12 (js/alpine.min.js)
//   - TomSelect and Litepicker from the Tabler 1.4.0 dist (libs/)
//   - Tabler Icons 3.45.0, outline subset (icons/*.svg)
//
//go:embed all:assets
var assetsFS embed.FS

var _ = assetsFS // consumed by the renderer (M1); keeps the embed compiled until then
