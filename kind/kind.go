// Package kind implements the result-kind registry.
//
// Implements notekit-rendering-contract.md. Every kind registers two halves, per
// the contract's two-forms rule: a durable writer (what persists, as plain
// CommonMark — an inline fence or a sidecar artifact plus reference) and a live
// renderer (what the browser shows during a session). A kind with no durable form
// is inadmissible.
//
// Both slots exist from the start, but they land at different milestones: package
// run needs the durable half from M1 in order to write a result block at all,
// while package serve uses the live half from M2. This is a correction to the kit
// spec's milestone list, which implies the whole registry arrives with serve.
//
// Core kinds, fixed for v1 and required of every tool: text, table (durable as csv
// or jsonl), and error. Registration is compile-time in the tool binary; there is
// no plugin loading.
//
// Anything that exists only live — colour, sortability, interactivity — must
// degrade to nothing. The live view may be prettier than the durable form, never
// fuller.
package kind
