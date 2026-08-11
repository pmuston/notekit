// Package serve provides the Echo handlers and HTMX templates for the notebook UI.
//
// Implements notekit-kit-spec.md §3.5: notebook view, cell run (post returning a run
// ID), run-status polling at 500 ms with a spinner, result rendering through the kind
// registry's live half, inline prose editing with an unsaved-changes indicator, and
// save.
//
// All assets are embedded with go:embed — no CDN, no network round-trip to render, no
// frontend build step (harvest R9). HTMX is vendored under assets/ with its version
// recorded in assets/VENDOR.md.
//
// # Components, not an application
//
// A [Server] registers its routes on an [echo.Echo] the caller owns, so a tool composes
// its own flags, executor registration, and middleware around it rather than inheriting
// a fixed application. [Server.Echo] is a convenience for the common case.
//
// # Live is prettier, never fuller
//
// ANSI colour, sortable tables, and click-to-edit prose exist only in the browser. Every
// one degrades to nothing: with JavaScript off the page still renders every result, and
// the durable form on disk stands alone on GitHub. Nothing rendered here is ever written
// back — the only writes are a cell run and an explicit prose save, both of which go
// through package run and package doc.
package serve

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/pmuston/notekit/doc"
	"github.com/pmuston/notekit/kind"
	"github.com/pmuston/notekit/run"
)

//go:embed assets
var assetFS embed.FS

//go:embed templates
var templateFS embed.FS

// DefaultPollInterval is how often the browser polls a running cell (harvest R3).
const DefaultPollInterval = 500 * time.Millisecond

// Server renders and drives one notebook.
type Server struct {
	sched    *run.Scheduler
	registry *kind.Registry
	path     string
	title    string
	lang     string
	base     string
	poll     time.Duration
	tmpl     *template.Template
	assets   fs.FS

	// width overrides the notebook's `width` key when non-empty, the same way
	// title does.
	width string
}

// widthFull is the one value §2.2 defines. Anything else means the default
// column, so a later spec can add values without this failing on them.
const widthFull = "full"

// wide reports whether the page should use the full window width: the WithWidth
// override if one was given, else the notebook's `width` key (§2.2).
//
// The key is read from the notebook rather than required from the tool because
// presentation belongs to the notebook — a wide notebook should be wide in every
// tool, not only in one that remembered to wire it up. It is safe to take from the
// file because honouring it can do no more than choose a layout.
func (s *Server) wide(nb *doc.Notebook) bool {
	if s.width != "" {
		return s.width == widthFull
	}
	return nb.Front()["width"] == widthFull
}

// Option configures a Server.
type Option func(*Server)

// WithBasePath mounts the UI under a prefix, for a tool that serves other things too.
func WithBasePath(base string) Option {
	return func(s *Server) {
		if base == "/" {
			base = ""
		}
		s.base = base
	}
}

// WithPollInterval overrides the polling cadence. Tests use a short one so a run's
// completion is observed without waiting half a second.
func WithPollInterval(d time.Duration) Option {
	return func(s *Server) {
		if d > 0 {
			s.poll = d
		}
	}
}

// WithLang overrides the language tag new cells are created with.
//
// It is rarely needed: the tag defaults to whatever the notebook's executor claims, asked
// of the scheduler, so the add-cell form cannot offer a tag nothing can run. Set it only
// when a tool wants something other than its executor's own tag.
func WithLang(lang string) Option {
	return func(s *Server) { s.lang = lang }
}

// WithWidth overrides the page width, which otherwise comes from the notebook's
// `width` front-matter key (§2.2). Pass "full" for the whole window, or anything
// else for the default reading column.
//
// A tool needs this only to force one or the other — leaving it unset lets each
// notebook choose, which is the intended behaviour.
func WithWidth(width string) Option {
	return func(s *Server) { s.width = width }
}

// WithTitle overrides the displayed title, which otherwise comes from the notebook's
// `title` front-matter key and falls back to the file name (§2).
func WithTitle(title string) Option {
	return func(s *Server) { s.title = title }
}

// WithRegistry supplies the kind registry whose live half renders results. Defaults to
// [kind.NewRegistry].
func WithRegistry(r *kind.Registry) Option {
	return func(s *Server) { s.registry = r }
}

// New returns a Server for an already-open notebook.
//
// The notebook must already be open in the scheduler: opening it is the tool's job,
// because only the tool knows which executor to hand it.
func New(sched *run.Scheduler, notebookPath string, opts ...Option) (*Server, error) {
	if sched == nil {
		return nil, fmt.Errorf("serve: a scheduler is required")
	}
	s := &Server{
		sched:    sched,
		registry: kind.NewRegistry(),
		path:     filepath.Clean(notebookPath),
		poll:     DefaultPollInterval,
	}
	for _, o := range opts {
		o(s)
	}

	tmpl, err := template.ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("serve: parsing templates: %w", err)
	}
	s.tmpl = tmpl

	sub, err := fs.Sub(assetFS, "assets")
	if err != nil {
		return nil, fmt.Errorf("serve: opening embedded assets: %w", err)
	}
	s.assets = sub

	// Confirm the notebook is open now rather than failing on the first request.
	if _, err := s.sched.Cells(s.path); err != nil {
		return nil, fmt.Errorf("serve: %w", err)
	}
	// Default the new-cell tag to the executor's own, so a tool cannot forget and end
	// up offering to create cells nothing can run.
	if s.lang == "" {
		lang, err := s.sched.Lang(s.path)
		if err != nil {
			return nil, fmt.Errorf("serve: %w", err)
		}
		s.lang = lang
	}
	return s, nil
}

// Register adds the server's routes to an Echo instance the caller owns.
func (s *Server) Register(e *echo.Echo) {
	g := e.Group(s.base)

	g.GET("", s.handleNotebook)
	g.GET("/", s.handleNotebook)
	g.POST("/cells/:index/run", s.handleRun)
	g.POST("/cells/run-all", s.handleRunAll)
	g.GET("/runs/:id", s.handleRunStatus)
	g.POST("/runs/:id/cancel", s.handleCancel)
	g.GET("/cells/:index/source", s.handleSourceGet)
	g.PUT("/cells/:index/source", s.handleSourcePut)
	g.POST("/cells/add", s.handleAddCell)
	g.DELETE("/cells/:index", s.handleDeleteCell)
	g.POST("/cells/:index/move-up", s.handleMoveUp)
	g.POST("/cells/:index/move-down", s.handleMoveDown)
	g.GET("/prose/:ref", s.handleProseGet)
	g.PUT("/prose/:ref", s.handleProsePut)
	g.GET("/sidecar/:name", s.handleSidecar)

	// Embedded assets, so a tool is a single static binary.
	g.GET("/assets/*", echo.WrapHandler(http.StripPrefix(s.base+"/assets/",
		http.FileServer(http.FS(s.assets)))))
}

// Echo returns a ready Echo instance with the server's routes and sensible middleware.
//
// A tool that wants different middleware builds its own Echo and calls
// [Server.Register] instead — this is the convenience, not the contract.
func (s *Server) Echo() *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.Recover())
	s.Register(e)
	return e
}

// render executes a named template to w.
func (s *Server) render(w io.Writer, name string, data any) error {
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		return fmt.Errorf("serve: rendering %s: %w", name, err)
	}
	return nil
}

// html writes a rendered template as an HTML response.
func (s *Server) html(c echo.Context, status int, name string, data any) error {
	c.Response().Header().Set(echo.HeaderContentType, echo.MIMETextHTMLCharsetUTF8)
	c.Response().WriteHeader(status)
	return s.render(c.Response(), name, data)
}
