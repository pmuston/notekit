// Package serve provides the Echo handlers and HTMX templates for the notebook UI.
//
// Implements notekit-kit-spec.md §3.5: notebook view, cell run (post returning a
// run ID), run-status polling at 500 ms with a spinner, result rendering through
// the kind registry's live half, inline prose editing with an unsaved-changes
// indicator, and save.
//
// All assets are embedded with go:embed — no CDN, no network round-trip to render,
// no frontend build step (harvest R9). Live-only niceties, ANSI colour and
// sortable tables for csv and jsonl, must never reach disk.
//
// This package provides components, not an application. Each tool composes its own
// main around them, supplying flags, executor registration, and session config.
// If a consumer cannot compose these without editing this package, the boundary is
// wrong and the package changes — that is what M3 exists to discover.
package serve
