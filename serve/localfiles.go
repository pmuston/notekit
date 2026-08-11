package serve

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
)

// localFilesKey is the front-matter key a notebook uses to declare that it
// displays files from its own directory (§2.4). It declares; it never grants.
const localFilesKey = "local-files"

// wantsLocalFiles reports whether the notebook declares the need (§2.4).
//
// Nothing acts on this but the UI, which uses it to explain a missing image. The
// decision to serve is [WithLocalFiles], which comes from the tool.
func wantsLocalFiles(front map[string]string) bool {
	return front[localFilesKey] == "true"
}

// handleLocalFile serves a file from the notebook's own directory.
//
// Registered only when the tool passed [WithLocalFiles], so when local files are
// off the route does not exist rather than answering 403 — there is nothing to
// probe.
//
// The notebook's directory is wherever the user keeps it, which may be a home
// directory or a repository root, so containment is the whole of this handler:
//
//   - dot-prefixed components are refused outright, which keeps `.git/config`
//     and `.env` unreachable and rejects `..` as a side effect;
//   - the path is cleaned against a rooted path before being joined, so `../`
//     cannot climb out lexically, percent-encoded or not;
//   - symbolic links are resolved and containment re-checked, because a link
//     inside the directory could otherwise point anywhere on disk;
//   - directories are refused, so there is no listing.
func (s *Server) handleLocalFile(c echo.Context) error {
	rel, err := url.PathUnescape(c.Param("*"))
	if err != nil || rel == "" {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}

	for _, part := range strings.Split(rel, "/") {
		if strings.HasPrefix(part, ".") {
			return echo.NewHTTPError(http.StatusNotFound, "not found")
		}
	}

	// Absolute before anything else. The notebook path is whatever the tool passed
	// — `clinote fig.md` leaves it relative — and comparing a relative resolved
	// path against an absolute base refuses everything, silently and always.
	base, err := filepath.Abs(filepath.Dir(s.path))
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "cannot resolve the notebook directory")
	}
	full := filepath.Join(base, filepath.Clean("/"+rel))

	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	baseResolved, err := filepath.EvalSymlinks(base)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "cannot resolve the notebook directory")
	}
	if resolved != baseResolved &&
		!strings.HasPrefix(resolved, baseResolved+string(os.PathSeparator)) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}

	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}

	// An image fetched through <img> cannot run its scripts, but navigating
	// straight to the URL loads it as a document, where an SVG can. The sandbox
	// directive makes it inert either way (§2.4).
	c.Response().Header().Set("Content-Security-Policy", "sandbox")
	c.Response().Header().Set("X-Content-Type-Options", "nosniff")
	return c.File(resolved)
}
