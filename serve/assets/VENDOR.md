# Vendored frontend assets

Committed, not fetched. Harvest R9: no CDN, no network round-trip to render, no
frontend build step. Everything here is embedded with `go:embed`, so a notekit tool
is a single static binary with no runtime file dependencies beyond the notebook and
its sidecar directory.

| File | Version | Source |
|---|---|---|
| `htmx.min.js` | 2.0.4 | https://unpkg.com/htmx.org@2.0.4/dist/htmx.min.js |

`notekit.css` and `notekit.js` are ours, not vendored.

Refresh with `make vendor`, then commit the result and update the version above. Read
the diff: a vendored asset that changed silently is a dependency upgrade nobody
reviewed.
