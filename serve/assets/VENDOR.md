# Vendored frontend assets

Committed, not fetched. Harvest R9: no CDN, no network round-trip to render, no
frontend build step. Everything here is embedded with `go:embed`, so a notekit tool
is a single static binary with no runtime file dependencies beyond the notebook and
its sidecar directory.

| File | Version | Licence | Source |
|---|---|---|---|
| `htmx.min.js` | 2.0.4 | Zero-Clause BSD (0BSD) | https://unpkg.com/htmx.org@2.0.4/dist/htmx.min.js |

`notekit.css` and `notekit.js` are ours, not vendored, and are covered by this
repository's MIT licence.

Record the licence of anything added here. htmx is 0BSD, which requires no attribution
at all — but a vendored file whose terms nobody wrote down is a problem waiting for
whoever ships this, and the minified build carries no header to check against.

Refresh with `make vendor`, then commit the result and update the version and licence
above. Read
the diff: a vendored asset that changed silently is a dependency upgrade nobody
reviewed.
