// Package server serves the JSON API and the embedded browser UI from one
// loopback port. Every mutation is POST, PATCH, PUT or DELETE and must
// declare a JSON content type, which an HTML form cannot send; a request
// with a Host other than loopback, or an Origin from anywhere but this
// server, is refused. So a page on another site cannot drive the API
// from the user's browser.
//
// The API speaks the store's own types: the outline is the same object
// `task list --json` prints, and every write is one Store method, so the
// CLI, the TUI and the browser cannot disagree about what a change means.
package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/thisisnic/tasktracker/internal/goallink"
	"github.com/thisisnic/tasktracker/internal/task"
)

//go:embed all:dist
var dist embed.FS

// Options configure a Server.
type Options struct {
	Store *task.Store
	// Goals reads goaltracker's database for the goals a project can
	// serve. Nil means goaltracker is not consulted.
	Goals   *goallink.Reader
	Version string
}

// Server is the API and UI handler.
type Server struct {
	store   *task.Store
	goals   *goallink.Reader
	version string
	mux     *http.ServeMux
	ui      http.Handler
	// writes counts the mutations this server has made. SQLite's data
	// version moves only when another connection commits, and the store
	// is one connection, so the server's own writes would not move it;
	// a page in a second tab would then never see what the first tab
	// did. The version the page polls carries both, and epoch, which
	// names this process: both counts start again when the server is
	// restarted, and a page left open across the restart would else
	// take the new process's first version for the one it loaded and
	// miss what was written while the server was down.
	writes atomic.Int64
	epoch  int64
}

// New builds a Server.
func New(o Options) *Server {
	s := &Server{store: o.Store, goals: o.Goals, version: o.Version, mux: http.NewServeMux(), epoch: time.Now().UnixNano()}
	files, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	s.ui = uiHandler(files)
	s.routes()
	return s
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /api/meta", s.meta)
	m.HandleFunc("GET /api/version", s.dataVersion)
	m.HandleFunc("GET /api/outline", s.outline)
	m.HandleFunc("GET /api/projects", s.listProjects)
	m.HandleFunc("GET /api/goals", s.listGoals)
	m.HandleFunc("POST /api/areas", s.addArea)
	m.HandleFunc("PATCH /api/areas/{id}", s.editArea)
	m.HandleFunc("DELETE /api/areas/{id}", s.deleteArea)
	m.HandleFunc("POST /api/projects", s.addProject)
	m.HandleFunc("PATCH /api/projects/{id}", s.editProject)
	m.HandleFunc("DELETE /api/projects/{id}", s.deleteProject)
	m.HandleFunc("POST /api/tasks", s.addTask)
	m.HandleFunc("PATCH /api/tasks/{id}", s.editTask)
	m.HandleFunc("PUT /api/tasks/{id}/archived", s.archiveTask)
	m.HandleFunc("POST /api/tasks/archive-finished", s.archiveFinished)
	m.HandleFunc("POST /api/tasks/{id}/copy", s.copyTask)
	m.HandleFunc("DELETE /api/tasks/{id}", s.deleteTask)
	m.HandleFunc("POST /api/tasks/{id}/subtasks", s.addSubtask)
	m.HandleFunc("PATCH /api/subtasks/{id}", s.editSubtask)
	m.HandleFunc("PUT /api/subtasks/{id}/done", s.tickSubtask)
	m.HandleFunc("DELETE /api/subtasks/{id}", s.deleteSubtask)
	m.HandleFunc("/api/", s.noSuchEndpoint)
	m.Handle("/", s.ui)
}

// noSuchEndpoint answers for paths under /api/ that no route claims. It
// matches every method, so a wrong method on a real route lands here
// too; that case is told apart and reported as 405, as the mux would
// have done without this catch-all.
func (s *Server) noSuchEndpoint(w http.ResponseWriter, r *http.Request) {
	var allow []string
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if method == r.Method {
			continue
		}
		probe := r.Clone(r.Context())
		probe.Method = method
		if _, pattern := s.mux.Handler(probe); pattern != "" && pattern != "/api/" && pattern != "/" {
			allow = append(allow, method)
		}
	}
	if len(allow) > 0 {
		w.Header().Set("Allow", strings.Join(allow, ", "))
		fail(w, http.StatusMethodNotAllowed, "method not allowed; use "+strings.Join(allow, " or "))
		return
	}
	fail(w, http.StatusNotFound, "no such endpoint")
}

// ServeHTTP refuses requests that did not come from a loopback name and
// cross-origin mutations, then dispatches.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The page is never shown inside another site's frame, where a
	// click on Delete and then Yes could be arranged from outside.
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	if !loopbackHost(r.Host) {
		// The listener only accepts loopback connections, but a page on
		// a name that re-resolves to 127.0.0.1 (DNS rebinding) reaches
		// it with that name as Host and Origin, and would otherwise pass
		// the same-origin check below. Reads are refused too: the tasks
		// are private.
		fail(w, http.StatusForbidden, "host not recognised")
		return
	}
	// The route is found first, so a mutation sent to no endpoint, or
	// with the wrong method, is answered 404 or 405 by noSuchEndpoint
	// whatever its headers say, and the page's files are served as
	// files. Only a real API route needs the checks below.
	_, pattern := s.mux.Handler(r)
	endpoint := pattern != "" && pattern != "/api/" && pattern != "/"
	read := r.Method == http.MethodGet || r.Method == http.MethodHead
	if pattern == "/" && !read {
		// The page and its files are only ever read; a form posted at
		// them from another site is not something to answer.
		w.Header().Set("Allow", "GET, HEAD")
		fail(w, http.StatusMethodNotAllowed, "method not allowed; use GET")
		return
	}
	if endpoint && !read {
		// Required even without a body: a form cannot send it, so this
		// holds on its own should a browser omit both headers below.
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			fail(w, http.StatusBadRequest, "Content-Type must be application/json")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r.Host) {
			fail(w, http.StatusForbidden, "cross-origin request refused")
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" {
			fail(w, http.StatusForbidden, "cross-site request refused")
			return
		}
	}
	if read || !endpoint {
		s.mux.ServeHTTP(w, r)
		return
	}
	// A mutation counts as a write unless it was refused: one that went
	// through did write, and one that failed on the server, as when the
	// browser gave up on the request and the row could not be read
	// back, may have, so every tab must come to show it; one the store
	// refused, 4xx, touched nothing, and a tab noting it as a change
	// from elsewhere would be wrong. It is counted after the handler
	// returns, so a poll in between sees the old count and the next one
	// reloads: late by a poll, never missed.
	sw := &statusWriter{ResponseWriter: w}
	s.mux.ServeHTTP(sw, r)
	if sw.status < 400 || sw.status >= 500 {
		s.writes.Add(1)
	}
}

// statusWriter remembers the status a handler wrote.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Write counts a body written with no header as the 200 it implies.
func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// loopbackHost reports whether a Host header names this machine's
// loopback address, with or without a port.
func loopbackHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	switch strings.ToLower(host) {
	case "127.0.0.1", "localhost", "::1", "[::1]":
		return true
	}
	return false
}

// sameOrigin reports whether an Origin header names this server, which
// is only ever reached by loopback.
func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" {
		return false
	}
	return u.Host == host
}

// uiHandler serves the Svelte build in files. A path that names no file
// gets the page when it looks like a route, with no extension, so a
// bookmark deeper than / still opens it; the page's asset paths are
// absolute, so it loads from there. A path that looks like a file gets
// 404, so a stale page asking for an asset from before an upgrade is
// told so rather than handed HTML. A directory is not listed. Without
// a build, every path gets a notice saying so.
func uiHandler(files fs.FS) http.Handler {
	if _, err := fs.Stat(files, "index.html"); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, "This build has no UI. Build one with `make build`, which builds the web app under web/ first, or use `tasktracker tui` or the CLI; the API is under /api/.\n")
		})
	}
	static := http.FileServer(http.FS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if info, err := fs.Stat(files, p); err != nil || info.IsDir() {
			if path.Ext(p) != "" {
				http.NotFound(w, r)
				return
			}
			r.URL.Path = "/"
		}
		static.ServeHTTP(w, r)
	})
}

// Listen binds the loopback port, refusing to start when it is taken: a
// second server over the same database is not needed, since the TUI and
// the CLI work beside a running one, and a fixed port keeps a home page
// bookmark working.
func Listen(port int) (net.Listener, error) {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if inUse(err) {
			return nil, fmt.Errorf("port %d is already in use; is another tasktracker serving? Use --port, or set port in the config, to serve on a different one", port)
		}
		return nil, err
	}
	return ln, nil
}

// Serve runs the server on ln until ctx ends.
func Serve(ctx context.Context, ln net.Listener, s *Server) error {
	srv := &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// failErr maps store errors to statuses: what the caller got wrong is
// a 4xx, anything else (SQLite, a cancelled context) a 500. A project
// or area the body names that does not exist matches both ErrInvalid
// and ErrNotFound, and is the body's fault: 400, so that 404 always
// means the thing at the address.
func failErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, task.ErrInvalid):
		fail(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, task.ErrNotFound):
		fail(w, http.StatusNotFound, err.Error())
	default:
		fail(w, http.StatusInternalServerError, err.Error())
	}
}

// readJSON decodes a request body; ServeHTTP has already required the
// JSON content type. A body is required: a mutation with nothing to say
// is a mistake, and {} says "no change" explicitly.
func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("bad request body: %w", err)
	}
	return nil
}

// idOf is the {id} of the path, which must be a positive integer.
func idOf(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("id must be a positive integer")
	}
	return id, nil
}

// --- reads ---

// Meta is what the UI shows about the binary serving it.
type Meta struct {
	Version string `json:"version"`
}

func (s *Server) meta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, Meta{Version: s.version})
}

// Version names the state of the database as this server has seen it:
// the server's epoch, SQLite's data version, which moves when another
// connection commits, and this server's own write count, so it moves
// on every write from anywhere and on a restart. The UI polls it and
// reloads the outline when it is not the one it loaded. The UI takes
// the first two parts as the state of the database and the last as the
// server's own doing, and compares no further.
type Version struct {
	Version string `json:"version"`
}

// dbVersion reads the version as of now.
func (s *Server) dbVersion(ctx context.Context) (string, error) {
	v, err := s.store.DataVersion(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d.%d.%d", s.epoch, v, s.writes.Load()), nil
}

func (s *Server) dataVersion(w http.ResponseWriter, r *http.Request) {
	v, err := s.dbVersion(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, Version{Version: v})
}

// OutlineResponse is the tree with the version it was read at, so the
// UI can tell a later poll's version from the one it already shows.
type OutlineResponse struct {
	Version string       `json:"version"`
	Outline task.Outline `json:"outline"`
}

// outline is the tree of areas, projects, tasks and subtasks: as `task
// list --json` prints it, and with ?all=1 as `--all` does, taking in
// finished projects and archived tasks.
func (s *Server) outline(w http.ResponseWriter, r *http.Request) {
	all, err := boolParam(r, "all")
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	// The version is read first: a write that lands between the two
	// reads leaves the outline newer than the version says, so the next
	// poll reloads it once more rather than miss the write.
	v, err := s.dbVersion(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	o, err := s.store.Outline(r.Context(), all)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, OutlineResponse{Version: v, Outline: o})
}

// boolParam reads a ?name= flag: absent, 0 and false are off; 1 and
// true are on.
func boolParam(r *http.Request, name string) (bool, error) {
	switch v := r.URL.Query().Get(name); v {
	case "", "0", "false":
		return false, nil
	case "1", "true":
		return true, nil
	default:
		return false, fmt.Errorf("%s must be 1 or 0", name)
	}
}

// listProjects is every project, finished ones included, for the task
// form's project field: a task can be moved anywhere.
func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	ps, err := s.store.ListProjects(r.Context(), task.ProjectFilter{All: true})
	if err != nil {
		failErr(w, err)
		return
	}
	if ps == nil {
		ps = []task.Project{}
	}
	writeJSON(w, http.StatusOK, ps)
}

// Goals is goaltracker's goals for the project form and the detail
// pane. Readable is false when goaltracker's database cannot be read,
// in which case Goals is empty and goal ids are shown bare.
type Goals struct {
	Readable bool            `json:"readable"`
	Goals    []goallink.Goal `json:"goals"`
}

func (s *Server) listGoals(w http.ResponseWriter, r *http.Request) {
	goals, err := s.readGoals(r.Context())
	writeJSON(w, http.StatusOK, Goals{Readable: err == nil, Goals: goals})
}

// readGoals lists goaltracker's goals, or says why it cannot. Without
// a reader the answer is the same as an unreadable database: the ids
// are all there is.
func (s *Server) readGoals(ctx context.Context) ([]goallink.Goal, error) {
	if s.goals == nil {
		return []goallink.Goal{}, goallink.ErrUnavailable
	}
	goals, err := s.goals.All(ctx)
	if err != nil {
		return []goallink.Goal{}, err
	}
	if goals == nil {
		goals = []goallink.Goal{}
	}
	return goals, nil
}

// --- areas ---

// areaBody is an area as the UI sends it: every field optional, so the
// same shape serves adding (where the store requires a name) and
// editing (where a missing field is left alone).
type areaBody struct {
	Name     *string `json:"name"`
	ParentID *int64  `json:"parent_id"` // 0 for the top level
}

func (s *Server) addArea(w http.ResponseWriter, r *http.Request) {
	var b areaBody
	if err := readJSON(r, &b); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	in := task.NewArea{}
	if b.Name != nil {
		in.Name = *b.Name
	}
	if b.ParentID != nil {
		in.ParentID = *b.ParentID
	}
	a, err := s.store.AddArea(r.Context(), in)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) editArea(w http.ResponseWriter, r *http.Request) {
	id, err := idOf(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	var b areaBody
	if err := readJSON(r, &b); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	a, err := s.store.UpdateArea(r.Context(), id, task.AreaEdit{Name: b.Name, ParentID: b.ParentID})
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) deleteArea(w http.ResponseWriter, r *http.Request) {
	id, err := idOf(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.DeleteArea(r.Context(), id); err != nil {
		failErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- projects ---

// newProjectBody is a project to add. A new project is always active,
// so there is no state field; sending one is refused as unknown.
type newProjectBody struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	AreaID      int64   `json:"area_id"`
	GoalIDs     []int64 `json:"goal_ids"`
}

func (s *Server) addProject(w http.ResponseWriter, r *http.Request) {
	var b newProjectBody
	if err := readJSON(r, &b); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	p, err := s.store.AddProject(r.Context(), task.NewProject{Name: b.Name, Description: b.Description, AreaID: b.AreaID, GoalIDs: b.GoalIDs})
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

// projectEditBody is the fields of a project to change; a missing field
// is left alone. An empty goal_ids list clears the links, and area_id 0
// moves the project out of any area.
type projectEditBody struct {
	Name        *string     `json:"name"`
	Description *string     `json:"description"`
	State       *task.State `json:"state"`
	AreaID      *int64      `json:"area_id"`
	GoalIDs     *[]int64    `json:"goal_ids"`
}

// stateOnly reports whether the body changes the state and nothing
// else: the UI stepping a project on, or shelving it.
func (b projectEditBody) stateOnly() bool {
	return b.State != nil && b.Name == nil && b.Description == nil && b.AreaID == nil && b.GoalIDs == nil
}

func (s *Server) editProject(w http.ResponseWriter, r *http.Request) {
	id, err := idOf(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	var b projectEditBody
	if err := readJSON(r, &b); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	var p task.Project
	if b.stateOnly() {
		// A state change on its own is one UPDATE, so it cannot write
		// back a name or an area read a moment before another process
		// changed them, as the TUI's space and x keys do with MarkProject.
		if err = s.store.MarkProject(r.Context(), id, *b.State); err == nil {
			p, err = s.store.GetProject(r.Context(), id)
		}
	} else {
		p, err = s.store.UpdateProject(r.Context(), id, task.ProjectEdit{Name: b.Name, Description: b.Description, State: b.State, AreaID: b.AreaID, GoalIDs: b.GoalIDs})
	}
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	id, err := idOf(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.DeleteProject(r.Context(), id); err != nil {
		failErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- tasks ---

// newTaskBody is a task to add. Due takes what ParseDue accepts, today
// and tomorrow included; issue what ParseIssue accepts. A new task is
// always todo.
type newTaskBody struct {
	ProjectID int64  `json:"project_id"`
	Title     string `json:"title"`
	Due       string `json:"due"`
	Issue     string `json:"issue"`
	Notes     string `json:"notes"`
}

func (s *Server) addTask(w http.ResponseWriter, r *http.Request) {
	var b newTaskBody
	if err := readJSON(r, &b); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	t, err := s.store.AddTask(r.Context(), task.NewTask{ProjectID: b.ProjectID, Title: b.Title, Due: b.Due, Issue: b.Issue, Notes: b.Notes})
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// taskEditBody is the fields of a task to change; a missing field is
// left alone, and an empty due, issue or notes clears it. Status is
// refused on a copy, which always starts as todo.
type taskEditBody struct {
	Title     *string      `json:"title"`
	Due       *string      `json:"due"`
	Issue     *string      `json:"issue"`
	Notes     *string      `json:"notes"`
	Status    *task.Status `json:"status"`
	ProjectID *int64       `json:"project_id"`
}

// edit is the body as the store takes it.
func (b taskEditBody) edit() task.TaskEdit {
	return task.TaskEdit{Title: b.Title, Due: b.Due, Issue: b.Issue, Notes: b.Notes, Status: b.Status, ProjectID: b.ProjectID}
}

// statusOnly reports whether the body changes the status and nothing
// else: the UI stepping a task on, or dropping it.
func (b taskEditBody) statusOnly() bool {
	return b.Status != nil && b.Title == nil && b.Due == nil && b.Issue == nil && b.Notes == nil && b.ProjectID == nil
}

func (s *Server) editTask(w http.ResponseWriter, r *http.Request) {
	id, err := idOf(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	var b taskEditBody
	if err := readJSON(r, &b); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	var t task.Task
	if b.statusOnly() {
		// A status change on its own is one UPDATE, so it cannot write
		// back a title or notes read a moment before another process
		// changed them. The form's edit, which sends several fields,
		// reads then writes, as the TUI's form does.
		if err = s.store.MarkTask(r.Context(), id, *b.Status); err == nil {
			t, err = s.store.GetTask(r.Context(), id)
		}
	} else {
		t, err = s.store.UpdateTask(r.Context(), id, b.edit())
	}
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// archivedCount is the answer to POST /api/tasks/archive-finished.
type archivedCount struct {
	Archived int64 `json:"archived"`
}

// archiveFinished puts every finished task in an active project away in
// one go and says how many went, for the page's one button; zero is an
// answer, not an error.
func (s *Server) archiveFinished(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.ArchiveFinished(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, archivedCount{Archived: n})
}

// archivedBody is the one field of PUT /api/tasks/{id}/archived.
type archivedBody struct {
	Archived bool `json:"archived"`
}

// archiveTask puts a finished task away or brings it back. The store
// refuses an open task inside its UPDATE, so a task finished elsewhere
// a moment ago is archived and one reopened a moment ago is not.
func (s *Server) archiveTask(w http.ResponseWriter, r *http.Request) {
	id, err := idOf(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	var b archivedBody
	if err := readJSON(r, &b); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.ArchiveTask(r.Context(), id, b.Archived); err != nil {
		failErr(w, err)
		return
	}
	t, err := s.store.GetTask(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// copyTask makes a new task from an existing one, notes and subtasks
// included, with the body's fields changed. The answer is the new task
// with its subtasks, unticked.
func (s *Server) copyTask(w http.ResponseWriter, r *http.Request) {
	id, err := idOf(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	var b taskEditBody
	if err := readJSON(r, &b); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	n, err := s.store.CopyTask(r.Context(), id, b.edit())
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, n)
}

func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request) {
	id, err := idOf(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.DeleteTask(r.Context(), id); err != nil {
		failErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- subtasks ---

// subtaskBody is a subtask's one editable field.
type subtaskBody struct {
	Title string `json:"title"`
}

func (s *Server) addSubtask(w http.ResponseWriter, r *http.Request) {
	id, err := idOf(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	var b subtaskBody
	if err := readJSON(r, &b); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	st, err := s.store.AddSubtask(r.Context(), id, b.Title)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, st)
}

func (s *Server) editSubtask(w http.ResponseWriter, r *http.Request) {
	id, err := idOf(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	var b subtaskBody
	if err := readJSON(r, &b); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	st, err := s.store.RenameSubtask(r.Context(), id, b.Title)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// doneBody is the one field of PUT /api/subtasks/{id}/done.
type doneBody struct {
	Done bool `json:"done"`
}

func (s *Server) tickSubtask(w http.ResponseWriter, r *http.Request) {
	id, err := idOf(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	var b doneBody
	if err := readJSON(r, &b); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.TickSubtask(r.Context(), id, b.Done); err != nil {
		failErr(w, err)
		return
	}
	st, err := s.store.GetSubtask(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) deleteSubtask(w http.ResponseWriter, r *http.Request) {
	id, err := idOf(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.DeleteSubtask(r.Context(), id); err != nil {
		failErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
