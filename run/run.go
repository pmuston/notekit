// Package run is the run scheduler.
//
// Implements notekit-kit-spec.md §3.4. A run request returns a run ID immediately
// and its state is pollable — running, done, or failed (harvest R3); there is no
// streaming. Runs within one notebook are serialised in request order because
// sessions are stateful, so concurrent cell execution within a notebook is
// forbidden. Different notebooks run concurrently.
//
// Capture is capped at 1 MiB (harvest R4) with ANSI stripped for the durable form
// and a truncation marker plus truncated flag appended per format spec §6.
//
// On completion this package builds the output or error block with canonical
// metadata (format, run, tool, status), applies fence-length safety, splices via
// package doc, and triggers auto-save (harvest R5). Results are volatile: a run
// replaces the cell's prior result blocks and overwrites sidecar files carrying
// the cell's id.
//
// This package also owns SIGINT and SIGTERM handling and calls every live
// session's destroy hook, so teardown is reliable.
package run
