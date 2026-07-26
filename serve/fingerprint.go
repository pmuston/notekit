package serve

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/pmuston/notekit/doc"
)

// FingerprintHeader carries the notebook structure a page was rendered from.
//
// Cells are addressed by index (`/cells/2/run`), and an index is only meaningful against
// the structure the client can see. Reordering (format spec §10 h) changes indices while
// changing nothing a reader would notice, so a page left open in another tab can ask to
// run cell 2 and get a different cell than the one it displays. Adding and deleting cells
// have always had the same hazard; a reorder makes it a one-click operation.
//
// Addressing by `id` would be the obvious fix and does not work: `id` is assigned lazily,
// only to cells producing sidecar results (§5.1), and neither shipping tool produces any —
// so in practice no cell has one. Assigning them eagerly would defeat the laziness that
// keeps hand-authored fences clean. Hence a fingerprint instead: it needs no identity where
// there is none, and costs no format surface.
const FingerprintHeader = "X-Notekit-Doc"

// fingerprint summarises the structure a cell index refers to: how many cells there are,
// in what order, and which source fence each one is.
//
// Deliberately *not* a hash of the whole file. Results change on every run, and a
// fingerprint that changed with them would reject a move merely because a cell had been run
// since the page loaded — a false alarm, since running a cell moves nothing. What matters is
// whether index i still means the same cell.
func fingerprint(nb *doc.Notebook, src []byte) string {
	h := sha256.New()
	for _, c := range nb.Cells() {
		h.Write([]byte(c.HeadingText))
		h.Write([]byte{0})
		h.Write(c.Source.In(src)) // the whole fence: info string and body
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// setFingerprint tells the client the structure it now holds. Every response that changed
// the notebook must call it, or the next request will be rejected as stale.
func setFingerprint(c echo.Context, src []byte) {
	nb, err := doc.Parse(src)
	if err != nil {
		return // nothing useful to say about a document that will not parse
	}
	c.Response().Header().Set(FingerprintHeader, fingerprint(nb, src))
}

// checkFingerprint rejects a request aimed at a structure the notebook no longer has.
//
// A request with no fingerprint at all is allowed through: curl, a test and any non-browser
// client have no page to be stale, and refusing them would make the server unusable outside
// the UI for no safety gain. The guard exists for a browser holding a rendered page, and
// the browser always sends one.
// It reads the notebook itself rather than taking a parse from the caller: the comparison is
// only meaningful against the file as it stands right now, and one handler gets its cells
// from the scheduler rather than from disk.
func (s *Server) checkFingerprint(c echo.Context) error {
	claimed := c.Request().Header.Get(FingerprintHeader)
	if claimed == "" {
		return nil
	}
	nb, src, err := s.notebook()
	if err != nil {
		return nil // a broken notebook is somebody else's error to report
	}
	if claimed == fingerprint(nb, src) {
		return nil
	}
	// Refresh rather than splice. The page is describing a document that no longer
	// exists, so the useful response is to show the current one.
	c.Response().Header().Set("HX-Refresh", "true")
	return echo.NewHTTPError(http.StatusConflict,
		"the notebook changed since this page was rendered, so nothing was done — reloading")
}
