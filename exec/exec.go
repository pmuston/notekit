// Package exec defines the executor and session contracts — the kit's domain
// boundary.
//
// Interfaces only, per notekit-kit-spec.md §3.3. An executor declares the
// info-string language tag it claims, owns an opaque session whose lifetime is
// tied to notebook open and close (harvest R1), and returns a typed result kind
// plus payload, or a typed error carrying the domain's message and an optional
// numeric status for the error block (format spec §7).
//
// Whether the engine behind a session is embedded (a pty child, an in-process
// store) or external (a network database) never surfaces above this boundary
// (harvest D5).
//
// Executors must honour cancellation and must never write to the notebook file.
// Persistence is package run's job, through package doc.
package exec
