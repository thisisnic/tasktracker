package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/thisisnic/tasktracker/internal/goallink"
	"github.com/thisisnic/tasktracker/internal/task"
)

// setup serves a store holding area "home" (#1) with project "house"
// (#1) in it, task "paint the hall" (#1) due 2026-01-10 with subtask
// "buy paint" (#1), and project "work" (#2) in no area with task
// "email accountant" (#2), undated. goals is a goaltracker database to
// read, or nil for none.
func setup(t *testing.T, goals *goallink.Reader) (*httptest.Server, *task.Store) {
	t.Helper()
	ctx := context.Background()
	s, err := task.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	home, err := s.AddArea(ctx, task.NewArea{Name: "home"})
	if err != nil {
		t.Fatal(err)
	}
	house, err := s.AddProject(ctx, task.NewProject{Name: "house", AreaID: home.ID, GoalIDs: []int64{3}})
	if err != nil {
		t.Fatal(err)
	}
	paint, err := s.AddTask(ctx, task.NewTask{ProjectID: house.ID, Title: "paint the hall", Due: "2026-01-10", Notes: "two coats"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSubtask(ctx, paint.ID, "buy paint"); err != nil {
		t.Fatal(err)
	}
	work, err := s.AddProject(ctx, task.NewProject{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddTask(ctx, task.NewTask{ProjectID: work.ID, Title: "email accountant"}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(Options{Store: s, Goals: goals, Version: "test"}))
	t.Cleanup(srv.Close)
	return srv, s
}

// goaltrackerDB writes a database shaped like goaltracker's, with goals
// #3 and #7.
func goaltrackerDB(t *testing.T) *goallink.Reader {
	t.Helper()
	path := filepath.Join(t.TempDir(), "goaltracker.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE goals (id INTEGER PRIMARY KEY, statement TEXT NOT NULL, period TEXT NOT NULL);
		INSERT INTO goals VALUES (3, 'run 500 km', '2026'), (7, 'finish the garden', '2026-Q3')`); err != nil {
		t.Fatal(err)
	}
	return goallink.New(path)
}

// do sends a request as the UI's client does: every mutation declares
// JSON, body or not. headers are name, value pairs; "Host" sets the
// request's Host rather than a header.
func do(t *testing.T, srv *httptest.Server, method, path string, body any, headers ...string) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rdr)
	if method != "GET" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i] == "Host" {
			req.Host = headers[i+1]
			continue
		}
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// want sends a request and decodes the answer into v, a pointer, failing
// unless the status is code. v is zeroed first: the answers leave empty
// fields out, and Unmarshal would keep an earlier answer's values in them.
func want(t *testing.T, srv *httptest.Server, code int, method, path string, body any, v any) {
	t.Helper()
	got, b := do(t, srv, method, path, body)
	if got != code {
		t.Fatalf("%s %s: %d %s, want %d", method, path, got, b, code)
	}
	if v != nil {
		rv := reflect.ValueOf(v).Elem()
		rv.Set(reflect.Zero(rv.Type()))
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
}

func TestMetaAndOutline(t *testing.T) {
	srv, _ := setup(t, nil)
	var m Meta
	want(t, srv, 200, "GET", "/api/meta", nil, &m)
	if m.Version != "test" {
		t.Errorf("meta = %+v", m)
	}

	var o OutlineResponse
	want(t, srv, 200, "GET", "/api/outline", nil, &o)
	if o.Version == "" {
		t.Error("outline has no version")
	}
	if len(o.Outline.Areas) != 1 || o.Outline.Areas[0].Area.Name != "home" || len(o.Outline.Areas[0].Projects) != 1 || len(o.Outline.Projects) != 1 {
		t.Fatalf("outline = %+v", o.Outline)
	}
	paint := o.Outline.Areas[0].Projects[0].Tasks[0]
	if paint.Task.Title != "paint the hall" || paint.Task.Due != "2026-01-10" || len(paint.Subtasks) != 1 || paint.Subtasks[0].Title != "buy paint" {
		t.Errorf("task = %+v", paint)
	}
	// Empty lists are [] in the JSON, as task list --json promises.
	_, raw := do(t, srv, "GET", "/api/outline", nil)
	if !strings.Contains(string(raw), `"subtasks":[]`) || strings.Contains(string(raw), "null") {
		t.Errorf("empty lists are not []: %s", raw)
	}
	if code, b := do(t, srv, "GET", "/api/outline?all=maybe", nil); code != 400 {
		t.Errorf("bad all: %d %s", code, b)
	}
}

func TestOutlineAllShowsFinished(t *testing.T) {
	srv, s := setup(t, nil)
	ctx := context.Background()
	if err := s.MarkProject(ctx, 2, task.Shelved); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkTask(ctx, 1, task.Finished); err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveTask(ctx, 1, true); err != nil {
		t.Fatal(err)
	}
	var o OutlineResponse
	want(t, srv, 200, "GET", "/api/outline", nil, &o)
	if len(o.Outline.Projects) != 0 || len(o.Outline.Areas[0].Projects[0].Tasks) != 0 {
		t.Errorf("without all: shelved project or archived task listed: %+v", o.Outline)
	}
	want(t, srv, 200, "GET", "/api/outline?all=1", nil, &o)
	if len(o.Outline.Projects) != 1 || o.Outline.Projects[0].Project.State != task.Shelved {
		t.Errorf("all: shelved project missing: %+v", o.Outline.Projects)
	}
	if ts := o.Outline.Areas[0].Projects[0].Tasks; len(ts) != 1 || !ts[0].Task.Archived {
		t.Errorf("all: archived task missing: %+v", ts)
	}

	// Every project, whatever its state, is offered for moving a task.
	var ps []task.Project
	want(t, srv, 200, "GET", "/api/projects", nil, &ps)
	if len(ps) != 2 || ps[1].State != task.Shelved {
		t.Errorf("projects = %+v", ps)
	}
}

// TestVersionMovesOnAnyWrite checks what the UI polls: the version
// moves when another process writes, and when this server writes, so a
// second tab sees what the first did; it stays put when nothing was
// written, a refused write included.
func TestVersionMovesOnAnyWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	s, err := task.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	srv := httptest.NewServer(New(Options{Store: s}))
	defer srv.Close()
	var v Version
	want(t, srv, 200, "GET", "/api/version", nil, &v)
	last := v.Version
	moved := func(what string, wantMoved bool) {
		t.Helper()
		want(t, srv, 200, "GET", "/api/version", nil, &v)
		if (v.Version != last) != wantMoved {
			t.Errorf("%s: version %q after %q, want moved=%v", what, v.Version, last, wantMoved)
		}
		last = v.Version
	}
	moved("nothing written", false)
	want(t, srv, 201, "POST", "/api/areas", map[string]any{"name": "garden"}, nil)
	moved("the server's own write", true)
	// A refused write touched nothing, and no tab should be told
	// otherwise.
	do(t, srv, "POST", "/api/areas", map[string]any{"name": ""})
	moved("a refused write", false)
	do(t, srv, "PATCH", "/api/areas/99", map[string]any{"name": "x"})
	moved("a write to a missing row", false)
	// A form posted at the page from another site is refused, and is
	// not a write either: it must not have every tab reload.
	if code, _ := do(t, srv, "POST", "/", nil, "Content-Type", "application/x-www-form-urlencoded"); code != 405 {
		t.Errorf("POST /: %d", code)
	}
	do(t, srv, "POST", "/api/nothing", map[string]any{})
	moved("requests that reach no endpoint", false)
	other, err := task.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.AddArea(context.Background(), task.NewArea{Name: "garage"}); err != nil {
		t.Fatal(err)
	}
	moved("a write elsewhere", true)
	// The outline carries the same version, so the UI can compare.
	var o OutlineResponse
	want(t, srv, 200, "GET", "/api/outline", nil, &o)
	if o.Version != last {
		t.Errorf("outline version %q, /api/version %q", o.Version, last)
	}
	// A restarted server over the same database reports a version a page
	// has not seen, even with every count the same, so the page reloads
	// and takes in what was written while the server was down.
	time.Sleep(time.Millisecond)
	again := httptest.NewServer(New(Options{Store: s}))
	defer again.Close()
	want(t, again, 200, "GET", "/api/version", nil, &v)
	if v.Version == last {
		t.Errorf("a restarted server reports the same version %q", v.Version)
	}
	if parts := strings.Split(v.Version, "."); len(parts) != 3 {
		t.Errorf("version %q, want epoch.data.writes", v.Version)
	}
}

func TestGoals(t *testing.T) {
	srv, _ := setup(t, goaltrackerDB(t))
	var g Goals
	want(t, srv, 200, "GET", "/api/goals", nil, &g)
	if !g.Readable || len(g.Goals) != 2 || g.Goals[0].ID != 3 || g.Goals[0].Statement != "run 500 km" || g.Goals[1].Period != "2026-Q3" {
		t.Errorf("goals = %+v", g)
	}

	// An unreadable database answers with no goals and says so, rather
	// than failing: the ids still show.
	srv, _ = setup(t, goallink.New(filepath.Join(t.TempDir(), "absent.db")))
	_, raw := do(t, srv, "GET", "/api/goals", nil)
	if string(bytes.TrimSpace(raw)) != `{"readable":false,"goals":[]}` {
		t.Errorf("unreadable goals = %s", raw)
	}
	srv, _ = setup(t, nil)
	_, raw = do(t, srv, "GET", "/api/goals", nil)
	if string(bytes.TrimSpace(raw)) != `{"readable":false,"goals":[]}` {
		t.Errorf("no reader goals = %s", raw)
	}
}

func TestAreas(t *testing.T) {
	srv, s := setup(t, nil)
	var a task.Area
	want(t, srv, 201, "POST", "/api/areas", map[string]any{"name": " garden ", "parent_id": 1}, &a)
	if a.ID != 2 || a.Name != "garden" || a.ParentID != 1 {
		t.Errorf("added = %+v", a)
	}
	if code, b := do(t, srv, "POST", "/api/areas", map[string]any{"name": ""}); code != 400 || !strings.Contains(string(b), "name is required") {
		t.Errorf("blank name: %d %s", code, b)
	}
	if code, b := do(t, srv, "POST", "/api/areas", map[string]any{}); code != 400 || !strings.Contains(string(b), "name is required") {
		t.Errorf("no name: %d %s", code, b)
	}
	if code, b := do(t, srv, "POST", "/api/areas", map[string]any{"name": "x", "parent_id": 99}); code != 400 || !strings.Contains(string(b), "area 99: not found") {
		t.Errorf("missing parent: %d %s", code, b)
	}
	if code, b := do(t, srv, "POST", "/api/areas", map[string]any{"name": "x", "colour": "red"}); code != 400 || !strings.Contains(string(b), "bad request body") {
		t.Errorf("unknown field: %d %s", code, b)
	}

	// A patch changes only what it names; parent_id 0 lifts the area to
	// the top. An area cannot go inside itself.
	want(t, srv, 200, "PATCH", "/api/areas/2", map[string]any{"name": "back garden"}, &a)
	if a.Name != "back garden" || a.ParentID != 1 {
		t.Errorf("renamed = %+v", a)
	}
	want(t, srv, 200, "PATCH", "/api/areas/2", map[string]any{"parent_id": 0}, &a)
	if a.ParentID != 0 {
		t.Errorf("lifted = %+v", a)
	}
	if code, b := do(t, srv, "PATCH", "/api/areas/1", map[string]any{"parent_id": 1}); code != 400 || !strings.Contains(string(b), "inside itself") {
		t.Errorf("into itself: %d %s", code, b)
	}
	if code, _ := do(t, srv, "PATCH", "/api/areas/99", map[string]any{"name": "x"}); code != 404 {
		t.Errorf("missing area: %d", code)
	}
	if code, b := do(t, srv, "PATCH", "/api/areas/0", map[string]any{"name": "x"}); code != 400 || !strings.Contains(string(b), "positive integer") {
		t.Errorf("bad id: %d %s", code, b)
	}

	// Deleting an area moves what was in it up a level.
	want(t, srv, 204, "DELETE", "/api/areas/1", nil, nil)
	if p, err := s.GetProject(context.Background(), 1); err != nil || p.AreaID != 0 {
		t.Errorf("project after its area went: %+v, %v", p, err)
	}
	if code, _ := do(t, srv, "DELETE", "/api/areas/1", nil); code != 404 {
		t.Errorf("deleting twice: %d", code)
	}
}

func TestProjects(t *testing.T) {
	srv, _ := setup(t, nil)
	var p task.Project
	want(t, srv, 201, "POST", "/api/projects", map[string]any{"name": "garden", "description": "dig it", "area_id": 1, "goal_ids": []int64{7, 3, 7}}, &p)
	if p.ID != 3 || p.Name != "garden" || p.Description != "dig it" || p.AreaID != 1 || p.State != task.Active || len(p.GoalIDs) != 2 || p.GoalIDs[0] != 3 {
		t.Errorf("added = %+v", p)
	}
	if code, b := do(t, srv, "POST", "/api/projects", map[string]any{"name": "x", "state": "done"}); code != 400 {
		t.Errorf("state on add: %d %s", code, b)
	}
	if code, b := do(t, srv, "POST", "/api/projects", map[string]any{"name": "x", "goal_ids": []int64{0}}); code != 400 || !strings.Contains(string(b), "goal id 0") {
		t.Errorf("bad goal: %d %s", code, b)
	}
	if code, b := do(t, srv, "POST", "/api/projects", map[string]any{"name": "x", "area_id": 99}); code != 400 || !strings.Contains(string(b), "area 99: not found") {
		t.Errorf("missing area: %d %s", code, b)
	}

	want(t, srv, 200, "PATCH", "/api/projects/3", map[string]any{"state": "shelved", "area_id": 0}, &p)
	if p.State != task.Shelved || p.AreaID != 0 || p.Name != "garden" || len(p.GoalIDs) != 2 {
		t.Errorf("edited = %+v", p)
	}
	// Stepping the state: state alone, which leaves everything else as
	// it is, as the TUI's keys do.
	want(t, srv, 200, "PATCH", "/api/projects/1", map[string]any{"state": "done"}, &p)
	if p.State != task.Done || p.Name != "house" || p.AreaID != 1 || len(p.GoalIDs) != 1 {
		t.Errorf("stepped = %+v", p)
	}
	if code, b := do(t, srv, "PATCH", "/api/projects/1", map[string]any{"state": "paused"}); code != 400 || !strings.Contains(string(b), "want active, done or shelved") {
		t.Errorf("bad state alone: %d %s", code, b)
	}
	if code, _ := do(t, srv, "PATCH", "/api/projects/99", map[string]any{"state": "done"}); code != 404 {
		t.Errorf("missing project, state alone: %d", code)
	}
	// goal_ids [] clears the links; null, like a missing field, leaves
	// them alone, as it does every other field.
	want(t, srv, 200, "PATCH", "/api/projects/3", map[string]any{"goal_ids": []int64{}}, &p)
	if len(p.GoalIDs) != 0 {
		t.Errorf("[] did not clear goals: %+v", p)
	}
	want(t, srv, 200, "PATCH", "/api/projects/1", map[string]any{"goal_ids": nil, "name": nil}, &p)
	if len(p.GoalIDs) != 1 || p.Name != "house" {
		t.Errorf("null changed something: %+v", p)
	}
	if code, b := do(t, srv, "PATCH", "/api/projects/3", map[string]any{"state": "paused"}); code != 400 || !strings.Contains(string(b), "want active, done or shelved") {
		t.Errorf("bad state: %d %s", code, b)
	}
	if code, _ := do(t, srv, "PATCH", "/api/projects/99", map[string]any{"name": "x"}); code != 404 {
		t.Errorf("missing project: %d", code)
	}

	want(t, srv, 204, "DELETE", "/api/projects/1", nil, nil)
	var o OutlineResponse
	want(t, srv, 200, "GET", "/api/outline", nil, &o)
	if len(o.Outline.Areas[0].Projects) != 0 {
		t.Errorf("project still listed: %+v", o.Outline.Areas[0].Projects)
	}
	if code, _ := do(t, srv, "DELETE", "/api/projects/1", nil); code != 404 {
		t.Errorf("deleting twice: %d", code)
	}
}

func TestTasks(t *testing.T) {
	srv, _ := setup(t, nil)
	var tk task.Task
	today := time.Now().Format("2006-01-02")
	want(t, srv, 201, "POST", "/api/tasks", map[string]any{"project_id": 1, "title": "fix the gate", "due": "today", "issue": "octocat/house#4", "notes": "\n\nhinge\n"}, &tk)
	if tk.ID != 3 || tk.ProjectID != 1 || tk.Status != task.Todo || tk.Due != today || tk.Issue != "https://github.com/octocat/house/issues/4" || tk.Notes != "hinge" {
		t.Errorf("added = %+v", tk)
	}
	if code, b := do(t, srv, "POST", "/api/tasks", map[string]any{"project_id": 1, "title": "x", "due": "soon"}); code != 400 || !strings.Contains(string(b), "want YYYY-MM-DD") {
		t.Errorf("bad due: %d %s", code, b)
	}
	if code, b := do(t, srv, "POST", "/api/tasks", map[string]any{"project_id": 1, "title": "x", "issue": "nope"}); code != 400 || !strings.Contains(string(b), "want a GitHub issue URL") {
		t.Errorf("bad issue: %d %s", code, b)
	}
	// A project the body names that does not exist is the body's fault,
	// not a missing address.
	if code, b := do(t, srv, "POST", "/api/tasks", map[string]any{"project_id": 99, "title": "x"}); code != 400 || !strings.Contains(string(b), "project 99: not found") {
		t.Errorf("missing project: %d %s", code, b)
	}
	if code, b := do(t, srv, "POST", "/api/tasks", map[string]any{"project_id": 1}); code != 400 || !strings.Contains(string(b), "title is required") {
		t.Errorf("no title: %d %s", code, b)
	}

	// The form's edit: several fields, empty ones clearing.
	want(t, srv, 200, "PATCH", "/api/tasks/3", map[string]any{"title": "fix the back gate", "due": "", "issue": "", "project_id": 2}, &tk)
	if tk.Title != "fix the back gate" || tk.Due != "" || tk.Issue != "" || tk.ProjectID != 2 || tk.Notes != "hinge" {
		t.Errorf("edited = %+v", tk)
	}
	// Stepping the status: status alone.
	want(t, srv, 200, "PATCH", "/api/tasks/3", map[string]any{"status": "done"}, &tk)
	if tk.Status != task.Finished || tk.Title != "fix the back gate" {
		t.Errorf("stepped = %+v", tk)
	}
	want(t, srv, 200, "PATCH", "/api/tasks/3", map[string]any{"status": "todo"}, &tk)
	if tk.Status != task.Todo {
		t.Errorf("stepped back = %+v", tk)
	}
	for _, bad := range []string{"paused", "doing"} {
		if code, b := do(t, srv, "PATCH", "/api/tasks/3", map[string]any{"status": bad}); code != 400 || !strings.Contains(string(b), "want todo, done or dropped") {
			t.Errorf("bad status %q alone: %d %s", bad, code, b)
		}
	}
	if code, b := do(t, srv, "PATCH", "/api/tasks/3", map[string]any{"status": "paused", "title": "x"}); code != 400 || !strings.Contains(string(b), "want todo, done or dropped") {
		t.Errorf("bad status with title: %d %s", code, b)
	}
	if code, _ := do(t, srv, "PATCH", "/api/tasks/99", map[string]any{"status": "done"}); code != 404 {
		t.Errorf("missing task, status alone: %d", code)
	}
	if code, _ := do(t, srv, "PATCH", "/api/tasks/99", map[string]any{"title": "x"}); code != 404 {
		t.Errorf("missing task: %d", code)
	}
	if code, b := do(t, srv, "PATCH", "/api/tasks/3", map[string]any{"project_id": 99}); code != 400 || !strings.Contains(string(b), "project 99: not found") {
		t.Errorf("moving to a missing project: %d %s", code, b)
	}
	if code, b := do(t, srv, "POST", "/api/tasks/3/copy", map[string]any{"project_id": 99}); code != 400 || !strings.Contains(string(b), "project 99: not found") {
		t.Errorf("copying to a missing project: %d %s", code, b)
	}

	// Archiving: refused while open, done when finished, undone by
	// reopening.
	if code, b := do(t, srv, "PUT", "/api/tasks/3/archived", map[string]any{"archived": true}); code != 400 || !strings.Contains(string(b), "still todo") {
		t.Errorf("archiving an open task: %d %s", code, b)
	}
	want(t, srv, 200, "PATCH", "/api/tasks/3", map[string]any{"status": "done"}, &tk)
	want(t, srv, 200, "PUT", "/api/tasks/3/archived", map[string]any{"archived": true}, &tk)
	if !tk.Archived || tk.Status != task.Finished {
		t.Errorf("archived = %+v", tk)
	}
	want(t, srv, 200, "PUT", "/api/tasks/3/archived", map[string]any{"archived": false}, &tk)
	if tk.Archived {
		t.Errorf("unarchived = %+v", tk)
	}
	want(t, srv, 200, "PUT", "/api/tasks/3/archived", map[string]any{"archived": true}, &tk)
	want(t, srv, 200, "PATCH", "/api/tasks/3", map[string]any{"status": "todo"}, &tk)
	if tk.Archived {
		t.Errorf("reopened task still archived: %+v", tk)
	}
	if code, _ := do(t, srv, "PUT", "/api/tasks/99/archived", map[string]any{"archived": true}); code != 404 {
		t.Errorf("archiving a missing task: %d", code)
	}

	want(t, srv, 204, "DELETE", "/api/tasks/3", nil, nil)
	if code, _ := do(t, srv, "DELETE", "/api/tasks/3", nil); code != 404 {
		t.Errorf("deleting twice: %d", code)
	}
}

// TestArchiveFinished puts the done and dropped tasks away in one call
// and leaves the open one listed; a second call has nothing to do.
func TestArchiveFinished(t *testing.T) {
	srv, _ := setup(t, nil)
	var tk task.Task
	want(t, srv, 201, "POST", "/api/tasks", map[string]any{"project_id": 1, "title": "clear the gutters"}, &tk)
	want(t, srv, 200, "PATCH", "/api/tasks/1", map[string]any{"status": "done"}, &tk)
	want(t, srv, 200, "PATCH", "/api/tasks/3", map[string]any{"status": "dropped"}, &tk)
	var n struct {
		Archived int `json:"archived"`
	}
	want(t, srv, 200, "POST", "/api/tasks/archive-finished", nil, &n)
	if n.Archived != 2 {
		t.Errorf("archived = %d, want 2", n.Archived)
	}
	// Tasks 1 and 3 are gone from house; the open task 2 is still in work.
	var o OutlineResponse
	want(t, srv, 200, "GET", "/api/outline", nil, &o)
	house, work := o.Outline.Areas[0].Projects[0], o.Outline.Projects[0]
	if len(house.Tasks) != 0 || len(work.Tasks) != 1 || work.Tasks[0].Task.ID != 2 {
		t.Errorf("tasks still listed: house %+v, work %+v", house.Tasks, work.Tasks)
	}
	n.Archived = -1
	want(t, srv, 200, "POST", "/api/tasks/archive-finished", nil, &n)
	if n.Archived != 0 {
		t.Errorf("second call archived = %d, want 0", n.Archived)
	}
}

func TestCopyTask(t *testing.T) {
	srv, _ := setup(t, nil)
	var n task.TaskNode
	want(t, srv, 201, "POST", "/api/tasks/1/copy", map[string]any{"title": "paint the stairs", "project_id": 2}, &n)
	if n.Task.ID != 3 || n.Task.Title != "paint the stairs" || n.Task.ProjectID != 2 || n.Task.Due != "2026-01-10" || n.Task.Notes != "two coats" || n.Task.Status != task.Todo {
		t.Errorf("copy = %+v", n.Task)
	}
	if len(n.Subtasks) != 1 || n.Subtasks[0].Title != "buy paint" || n.Subtasks[0].Done || n.Subtasks[0].TaskID != 3 {
		t.Errorf("copied subtasks = %+v", n.Subtasks)
	}
	if code, b := do(t, srv, "POST", "/api/tasks/1/copy", map[string]any{"status": "done"}); code != 400 || !strings.Contains(string(b), "always starts as todo") {
		t.Errorf("copy with status: %d %s", code, b)
	}
	if code, _ := do(t, srv, "POST", "/api/tasks/99/copy", map[string]any{}); code != 404 {
		t.Errorf("copying a missing task: %d", code)
	}
}

func TestSubtasks(t *testing.T) {
	srv, _ := setup(t, nil)
	var st task.Subtask
	want(t, srv, 201, "POST", "/api/tasks/1/subtasks", map[string]any{"title": "move furniture"}, &st)
	if st.ID != 2 || st.TaskID != 1 || st.Title != "move furniture" || st.Done {
		t.Errorf("added = %+v", st)
	}
	if code, b := do(t, srv, "POST", "/api/tasks/1/subtasks", map[string]any{"title": " "}); code != 400 || !strings.Contains(string(b), "title is required") {
		t.Errorf("blank title: %d %s", code, b)
	}
	if code, _ := do(t, srv, "POST", "/api/tasks/99/subtasks", map[string]any{"title": "x"}); code != 404 {
		t.Errorf("missing task: %d", code)
	}

	want(t, srv, 200, "PATCH", "/api/subtasks/2", map[string]any{"title": "move the furniture"}, &st)
	if st.Title != "move the furniture" {
		t.Errorf("renamed = %+v", st)
	}
	if code, b := do(t, srv, "PATCH", "/api/subtasks/2", map[string]any{"done": true}); code != 400 || !strings.Contains(string(b), "bad request body") {
		t.Errorf("done is not a patch field: %d %s", code, b)
	}
	want(t, srv, 200, "PUT", "/api/subtasks/2/done", map[string]any{"done": true}, &st)
	if !st.Done || st.Title != "move the furniture" {
		t.Errorf("ticked = %+v", st)
	}
	want(t, srv, 200, "PUT", "/api/subtasks/2/done", map[string]any{"done": false}, &st)
	if st.Done {
		t.Errorf("unticked = %+v", st)
	}
	if code, _ := do(t, srv, "PUT", "/api/subtasks/99/done", map[string]any{"done": true}); code != 404 {
		t.Errorf("ticking a missing subtask: %d", code)
	}
	if code, _ := do(t, srv, "PATCH", "/api/subtasks/99", map[string]any{"title": "x"}); code != 404 {
		t.Errorf("renaming a missing subtask: %d", code)
	}

	want(t, srv, 204, "DELETE", "/api/subtasks/2", nil, nil)
	if code, _ := do(t, srv, "DELETE", "/api/subtasks/2", nil); code != 404 {
		t.Errorf("deleting twice: %d", code)
	}
}

func TestCrossOriginRefused(t *testing.T) {
	srv, _ := setup(t, nil)
	tick := map[string]any{"done": true}
	if code, _ := do(t, srv, "PUT", "/api/subtasks/1/done", tick, "Origin", "http://evil.example"); code != 403 {
		t.Errorf("evil origin: %d", code)
	}
	if code, _ := do(t, srv, "PUT", "/api/subtasks/1/done", tick, "Sec-Fetch-Site", "cross-site"); code != 403 {
		t.Errorf("cross-site: %d", code)
	}
	host := strings.TrimPrefix(srv.URL, "http://")
	if code, _ := do(t, srv, "PUT", "/api/subtasks/1/done", tick, "Origin", "http://"+host); code != 200 {
		t.Errorf("own origin: %d", code)
	}
	if code, _ := do(t, srv, "GET", "/api/outline", nil, "Origin", "http://evil.example"); code != 200 {
		t.Errorf("reads are not blocked by origin: %d", code)
	}
	// A form's content type is refused on every mutation, body or not.
	if code, _ := do(t, srv, "PUT", "/api/subtasks/1/done", tick, "Content-Type", "application/x-www-form-urlencoded"); code != 400 {
		t.Errorf("form content type: %d", code)
	}
	if code, _ := do(t, srv, "DELETE", "/api/subtasks/1", nil, "Content-Type", ""); code != 400 {
		t.Errorf("no content type: %d", code)
	}
	// A mutation with no body at all is a mistake, not "no change".
	if code, b := do(t, srv, "PATCH", "/api/areas/1", nil); code != 400 || !strings.Contains(string(b), "bad request body") {
		t.Errorf("no body: %d %s", code, b)
	}
}

func TestForeignHostRefused(t *testing.T) {
	srv, _ := setup(t, nil)
	if code, _ := do(t, srv, "GET", "/api/outline", nil, "Host", "evil.example:7344"); code != 403 {
		t.Errorf("foreign host: %d", code)
	}
	for _, h := range []string{"localhost:7344", "127.0.0.1", "[::1]:7344", "LOCALHOST"} {
		if code, _ := do(t, srv, "GET", "/api/outline", nil, "Host", h); code != 200 {
			t.Errorf("host %q: %d", h, code)
		}
	}
}

func TestUnknownRouteAndMethod(t *testing.T) {
	srv, _ := setup(t, nil)
	if code, b := do(t, srv, "GET", "/api/nothing", nil); code != 404 || !strings.Contains(string(b), "no such endpoint") {
		t.Errorf("unknown route: %d %s", code, b)
	}
	if code, b := do(t, srv, "POST", "/api/outline", map[string]any{}); code != 405 || !strings.Contains(string(b), "use GET") {
		t.Errorf("wrong method: %d %s", code, b)
	}
	if code, _ := do(t, srv, "GET", "/api/tasks/1/copy", nil); code != 405 {
		t.Errorf("GET on a POST route: %d", code)
	}
	// Without a JSON content type the answer is still about the route,
	// not the headers: there is no endpoint to protect.
	if code, b := do(t, srv, "DELETE", "/api/outline", nil, "Content-Type", ""); code != 405 || !strings.Contains(string(b), "use GET") {
		t.Errorf("wrong method, no content type: %d %s", code, b)
	}
	if code, b := do(t, srv, "POST", "/api/nothing", nil, "Content-Type", ""); code != 404 || !strings.Contains(string(b), "no such endpoint") {
		t.Errorf("unknown route, no content type: %d %s", code, b)
	}
}

// TestUI checks the page handler on its own, since whether the test
// binary embeds a built page depends on whether make web has run.
func TestUI(t *testing.T) {
	// Every answer, the page's included, refuses to be framed by another
	// site.
	srv, _ := setup(t, nil)
	for _, path := range []string{"/", "/api/outline"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if xfo, csp := resp.Header.Get("X-Frame-Options"), resp.Header.Get("Content-Security-Policy"); xfo != "DENY" || csp != "frame-ancestors 'none'" {
			t.Errorf("%s: X-Frame-Options %q, Content-Security-Policy %q", path, xfo, csp)
		}
	}
	// Without a build, the page says so.
	none := httptest.NewServer(uiHandler(fstest.MapFS{}))
	defer none.Close()
	if code, b := do(t, none, "GET", "/", nil); code != 503 || !strings.Contains(string(b), "no UI") {
		t.Errorf("no build: %d %s", code, b)
	}
	// With one, files are served as files and any other path gets the
	// page, so a bookmark deeper than / still opens it.
	built := httptest.NewServer(uiHandler(fstest.MapFS{
		"index.html":       {Data: []byte("<html>page</html>")},
		"assets/app.js":    {Data: []byte("js")},
		"assets/style.css": {Data: []byte("css")},
	}))
	defer built.Close()
	for path, want := range map[string]string{"/": "<html>page</html>", "/assets/app.js": "js", "/elsewhere": "<html>page</html>", "/deeper/still": "<html>page</html>", "/assets": "<html>page</html>"} {
		if code, b := do(t, built, "GET", path, nil); code != 200 || string(b) != want {
			t.Errorf("%s: %d %q, want %q", path, code, b, want)
		}
	}
	// A file that is not there is not there, so a page from before an
	// upgrade asking for its old assets is told so rather than given HTML.
	if code, _ := do(t, built, "GET", "/assets/old.js", nil); code != 404 {
		t.Errorf("missing asset: %d", code)
	}
}

func TestListenRefusesTakenPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if _, err := Listen(port); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Errorf("Listen on a taken port: %v", err)
	}
}

func TestServeStopsWithContext(t *testing.T) {
	srv, s := setup(t, nil)
	srv.Close()
	ln, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, New(Options{Store: s})) }()
	resp, err := http.Get("http://" + ln.Addr().String() + "/api/version")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop")
	}
}
