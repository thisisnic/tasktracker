package task

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// addProject, addTask and addSubtask are setup steps that fail the test
// rather than returning an error, for tests that are about something else.
func addProject(t *testing.T, s *Store, in NewProject) Project {
	t.Helper()
	p, err := s.AddProject(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func addTask(t *testing.T, s *Store, in NewTask) Task {
	t.Helper()
	task, err := s.AddTask(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func addSubtask(t *testing.T, s *Store, taskID int64, title string) Subtask {
	t.Helper()
	st, err := s.AddSubtask(context.Background(), taskID, title)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// check fails the test on err, for setup steps with no result.
func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "tasktracker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestProjectLifecycle(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	p, err := s.AddProject(ctx, NewProject{Name: "  house ", Description: " fix it up "})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != 1 || p.Name != "house" || p.Description != "fix it up" || p.State != Active || p.CreatedAt.IsZero() {
		t.Errorf("added project = %+v", p)
	}
	if _, err := s.AddProject(ctx, NewProject{Name: " "}); err == nil {
		t.Error("blank name accepted")
	}

	name, desc := "home", ""
	p, err = s.UpdateProject(ctx, p.ID, ProjectEdit{Name: &name, Description: &desc})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "home" || p.Description != "" {
		t.Errorf("updated project = %+v", p)
	}
	done := Done
	p, err = s.UpdateProject(ctx, p.ID, ProjectEdit{State: &done})
	if err != nil || p.State != Done || p.Name != "home" {
		t.Errorf("state edit = %+v, %v", p, err)
	}
	bad := State("paused")
	if _, err := s.UpdateProject(ctx, p.ID, ProjectEdit{State: &bad}); err == nil {
		t.Error("bad state accepted on edit")
	}
	blank := ""
	if _, err := s.UpdateProject(ctx, p.ID, ProjectEdit{Name: &blank}); err == nil {
		t.Error("blank name accepted on edit")
	}
	if _, err := s.UpdateProject(ctx, 99, ProjectEdit{Name: &name}); !errors.Is(err, ErrNotFound) {
		t.Errorf("edit missing = %v", err)
	}

	if err := s.MarkProject(ctx, p.ID, Shelved); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkProject(ctx, p.ID, State("paused")); err == nil {
		t.Error("bad state accepted")
	}
	if err := s.MarkProject(ctx, 99, Done); !errors.Is(err, ErrNotFound) {
		t.Errorf("mark missing = %v", err)
	}
	active, _ := s.ListProjects(ctx, ProjectFilter{})
	all, _ := s.ListProjects(ctx, ProjectFilter{All: true})
	shelved, _ := s.ListProjects(ctx, ProjectFilter{State: Shelved})
	if len(active) != 0 || len(all) != 1 || len(shelved) != 1 || shelved[0].State != Shelved {
		t.Errorf("lists: active=%v all=%v shelved=%v", active, all, shelved)
	}

	if err := s.DeleteProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetProject(ctx, p.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get deleted = %v", err)
	}
	if err := s.DeleteProject(ctx, p.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete twice = %v", err)
	}
}

func TestTaskLifecycle(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	p := addProject(t, s, NewProject{Name: "house"})
	other := addProject(t, s, NewProject{Name: "work"})
	task, err := s.AddTask(ctx, NewTask{ProjectID: p.ID, Title: " paint the hall ", Due: "2026-10-01", Notes: "\n\n  two coats\nwhite  \n\n"})
	if err != nil {
		t.Fatal(err)
	}
	if task.ID != 1 || task.ProjectID != p.ID || task.Title != "paint the hall" || task.Status != Todo || task.Due != "2026-10-01" {
		t.Errorf("added task = %+v", task)
	}
	// Blank lines at either end go; a leading indent and inner lines stay.
	if task.Notes != "  two coats\nwhite" {
		t.Errorf("notes = %q", task.Notes)
	}
	if _, err := s.AddTask(ctx, NewTask{ProjectID: p.ID, Title: ""}); err == nil {
		t.Error("blank title accepted")
	}
	if _, err := s.AddTask(ctx, NewTask{ProjectID: p.ID, Title: "x", Due: "soon"}); err == nil {
		t.Error("bad due accepted")
	}
	if _, err := s.AddTask(ctx, NewTask{ProjectID: 99, Title: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing project = %v", err)
	}

	title, due, finished := "paint the hallway", "", Finished
	task, err = s.UpdateTask(ctx, task.ID, TaskEdit{Title: &title, Due: &due, Status: &finished, ProjectID: &other.ID})
	if err != nil {
		t.Fatal(err)
	}
	if task.Title != "paint the hallway" || task.Due != "" || task.Status != Finished || task.ProjectID != other.ID || task.Notes != "  two coats\nwhite" {
		t.Errorf("updated task = %+v", task)
	}
	notes := "one coat\n"
	if task, err = s.UpdateTask(ctx, task.ID, TaskEdit{Notes: &notes}); err != nil || task.Notes != "one coat" || task.Title != "paint the hallway" {
		t.Errorf("notes edit = %+v, %v", task, err)
	}
	none := "\n  \n"
	if task, err = s.UpdateTask(ctx, task.ID, TaskEdit{Notes: &none}); err != nil || task.Notes != "" {
		t.Errorf("clearing the notes = %+v, %v", task, err)
	}
	blocked := Status("blocked")
	if _, err := s.UpdateTask(ctx, task.ID, TaskEdit{Status: &blocked}); err == nil {
		t.Error("bad status accepted on edit")
	}
	bad := int64(99)
	if _, err := s.UpdateTask(ctx, task.ID, TaskEdit{ProjectID: &bad}); !errors.Is(err, ErrNotFound) {
		t.Errorf("move to missing project = %v", err)
	}

	if err := s.MarkTask(ctx, task.ID, Dropped); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Status{"blocked", "doing"} {
		if err := s.MarkTask(ctx, task.ID, bad); err == nil {
			t.Errorf("status %q accepted", bad)
		}
	}
	if got, _ := s.GetTask(ctx, task.ID); got.Status != Dropped {
		t.Errorf("status = %s", got.Status)
	}
	if err := s.DeleteTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTask(ctx, task.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete twice = %v", err)
	}
}

func TestListTasksFilters(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	p := addProject(t, s, NewProject{Name: "a"})
	q := addProject(t, s, NewProject{Name: "b"})
	t1 := addTask(t, s, NewTask{ProjectID: p.ID, Title: "later", Due: "2026-12-01"})
	t2 := addTask(t, s, NewTask{ProjectID: q.ID, Title: "sooner", Due: "2026-10-01"})
	t3 := addTask(t, s, NewTask{ProjectID: p.ID, Title: "whenever"})
	t4 := addTask(t, s, NewTask{ProjectID: p.ID, Title: "finished", Due: "2026-09-01"})
	check(t, s.MarkTask(ctx, t4.ID, Finished))
	check(t, s.ArchiveTask(ctx, t4.ID, true))

	ids := func(ts []Task) []int64 {
		var out []int64
		for _, t := range ts {
			out = append(out, t.ID)
		}
		return out
	}
	cases := []struct {
		f    TaskFilter
		want []int64
	}{
		{TaskFilter{}, []int64{t1.ID, t3.ID, t4.ID, t2.ID}},
		{TaskFilter{ProjectID: q.ID}, []int64{t2.ID}},
		{TaskFilter{Status: Finished}, []int64{t4.ID}},
		{TaskFilter{Open: true}, []int64{t1.ID, t3.ID, t2.ID}},
		{TaskFilter{Unarchived: true}, []int64{t1.ID, t3.ID, t2.ID}},
		{TaskFilter{Status: Finished, Unarchived: true}, nil},
		{TaskFilter{Due: true}, []int64{t4.ID, t2.ID, t1.ID}},
		{TaskFilter{Due: true, Open: true}, []int64{t2.ID, t1.ID}},
	}
	for _, c := range cases {
		got, err := s.ListTasks(ctx, c.f)
		if err != nil || !reflect.DeepEqual(ids(got), c.want) {
			t.Errorf("ListTasks(%+v) = %v, %v; want %v", c.f, ids(got), err, c.want)
		}
	}
}

func TestCopyTask(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	house := addProject(t, s, NewProject{Name: "house"})
	work := addProject(t, s, NewProject{Name: "work"})
	orig := addTask(t, s, NewTask{ProjectID: house.ID, Title: "paint the hall", Due: "2026-10-01", Notes: "two coats"})
	buy := addSubtask(t, s, orig.ID, "buy paint")
	addSubtask(t, s, orig.ID, "move furniture")
	check(t, s.TickSubtask(ctx, buy.ID, true))
	check(t, s.MarkTask(ctx, orig.ID, Finished))

	// A plain copy: same title, due, notes and project, todo whatever the
	// original's status, subtasks unticked.
	got, err := s.CopyTask(ctx, orig.ID, TaskEdit{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Task.ID == orig.ID || got.Task.Title != "paint the hall" || got.Task.Due != "2026-10-01" || got.Task.Notes != "two coats" || got.Task.ProjectID != house.ID || got.Task.Status != Todo {
		t.Errorf("copy = %+v", got.Task)
	}
	if len(got.Subtasks) != 2 || got.Subtasks[0].Title != "buy paint" || got.Subtasks[0].Done || got.Subtasks[1].Title != "move furniture" || got.Subtasks[0].TaskID != got.Task.ID {
		t.Errorf("copied subtasks = %+v", got.Subtasks)
	}
	if o, _ := s.GetTask(ctx, orig.ID); o.Status != Finished {
		t.Errorf("original changed: %+v", o)
	}
	if subs, _ := s.ListSubtasks(ctx, orig.ID); len(subs) != 2 || !subs[0].Done {
		t.Errorf("original subtasks changed: %+v", subs)
	}

	// Overrides replace the copied fields.
	title, due, notes := " paint the landing ", "none", "\none coat\n\n"
	got, err = s.CopyTask(ctx, orig.ID, TaskEdit{Title: &title, Due: &due, Notes: &notes, ProjectID: &work.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.Task.Title != "paint the landing" || got.Task.Due != "" || got.Task.Notes != "one coat" || got.Task.ProjectID != work.ID {
		t.Errorf("copy with overrides = %+v", got.Task)
	}
	blank, bad, missing := "  ", "soon", int64(99)
	if _, err := s.CopyTask(ctx, orig.ID, TaskEdit{Title: &blank}); err == nil {
		t.Error("blank title accepted")
	}
	if _, err := s.CopyTask(ctx, orig.ID, TaskEdit{Due: &bad}); err == nil {
		t.Error("bad due accepted")
	}
	done := Finished
	if _, err := s.CopyTask(ctx, orig.ID, TaskEdit{Status: &done}); err == nil || !strings.Contains(err.Error(), "starts as todo") {
		t.Errorf("status override: %v", err)
	}
	if _, err := s.CopyTask(ctx, orig.ID, TaskEdit{ProjectID: &missing}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing project = %v", err)
	}
	if _, err := s.CopyTask(ctx, 99, TaskEdit{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing task = %v", err)
	}
	// A task with no subtasks copies to one with an empty, non-nil list.
	plain := addTask(t, s, NewTask{ProjectID: house.ID, Title: "fix the gate"})
	if got, err := s.CopyTask(ctx, plain.ID, TaskEdit{}); err != nil || got.Subtasks == nil || len(got.Subtasks) != 0 {
		t.Errorf("copy of a task with no subtasks: %+v, %v", got, err)
	}
}

func TestTaskIssue(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	p := addProject(t, s, NewProject{Name: "house"})
	tk := addTask(t, s, NewTask{ProjectID: p.ID, Title: "fix the gate", Issue: "owner/repo#42"})
	if tk.Issue != "https://github.com/owner/repo/issues/42" {
		t.Errorf("added issue = %q", tk.Issue)
	}
	if _, err := s.AddTask(ctx, NewTask{ProjectID: p.ID, Title: "x", Issue: "the gate one"}); err == nil {
		t.Error("bad issue accepted on add")
	}

	// An edit that leaves the issue alone keeps it.
	title := "fix the side gate"
	if got, err := s.UpdateTask(ctx, tk.ID, TaskEdit{Title: &title}); err != nil || got.Issue != tk.Issue {
		t.Errorf("rename: %+v, %v", got, err)
	}
	link := "https://github.com/owner/repo/pull/7"
	if got, err := s.UpdateTask(ctx, tk.ID, TaskEdit{Issue: &link}); err != nil || got.Issue != link {
		t.Errorf("new issue: %+v, %v", got, err)
	}
	bad := "soon"
	if _, err := s.UpdateTask(ctx, tk.ID, TaskEdit{Issue: &bad}); err == nil {
		t.Error("bad issue accepted on edit")
	}
	if got, _ := s.GetTask(ctx, tk.ID); got.Issue != link {
		t.Errorf("a refused edit changed the issue: %q", got.Issue)
	}

	// A copy has no issue unless it is given one.
	n, err := s.CopyTask(ctx, tk.ID, TaskEdit{})
	if err != nil || n.Task.Issue != "" {
		t.Errorf("plain copy: %+v, %v", n.Task, err)
	}
	other := "owner/repo#43"
	if n, err := s.CopyTask(ctx, tk.ID, TaskEdit{Issue: &other}); err != nil || n.Task.Issue != "https://github.com/owner/repo/issues/43" {
		t.Errorf("copy with an issue: %+v, %v", n.Task, err)
	}
	if _, err := s.CopyTask(ctx, tk.ID, TaskEdit{Issue: &bad}); err == nil {
		t.Error("bad issue accepted on copy")
	}

	none := ""
	if got, err := s.UpdateTask(ctx, tk.ID, TaskEdit{Issue: &none}); err != nil || got.Issue != "" {
		t.Errorf("clearing the issue: %+v, %v", got, err)
	}
}

func TestArchive(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	p := addProject(t, s, NewProject{Name: "house"})
	tk := addTask(t, s, NewTask{ProjectID: p.ID, Title: "paint"})

	// An open task cannot be put away.
	if err := s.ArchiveTask(ctx, tk.ID, true); err == nil || !strings.Contains(err.Error(), "still todo") || !errors.Is(err, ErrInvalid) {
		t.Errorf("archiving an open task: %v", err)
	}
	if err := s.ArchiveTask(ctx, 99, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("archiving a missing task: %v", err)
	}
	check(t, s.MarkTask(ctx, tk.ID, Finished))
	check(t, s.ArchiveTask(ctx, tk.ID, true))
	if got, _ := s.GetTask(ctx, tk.ID); !got.Archived || got.Status != Finished {
		t.Errorf("archived task = %+v", got)
	}
	tree, err := s.Tree(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree[0].Tasks) != 0 {
		t.Errorf("archived task still in the tree: %+v", tree[0].Tasks)
	}
	if all, _ := s.Tree(ctx, true); len(all[0].Tasks) != 1 {
		t.Errorf("archived task missing from the full tree: %+v", all)
	}

	// Reopening brings a task back, whichever way it is reopened.
	check(t, s.MarkTask(ctx, tk.ID, Todo))
	if got, _ := s.GetTask(ctx, tk.ID); got.Archived {
		t.Error("marking todo left the task archived")
	}
	check(t, s.MarkTask(ctx, tk.ID, Dropped))
	check(t, s.ArchiveTask(ctx, tk.ID, true))
	check(t, s.MarkTask(ctx, tk.ID, Finished)) // finished to finished: stays archived
	if got, _ := s.GetTask(ctx, tk.ID); !got.Archived {
		t.Error("marking done unarchived the task")
	}
	todo := Todo
	if got, err := s.UpdateTask(ctx, tk.ID, TaskEdit{Status: &todo}); err != nil || got.Archived {
		t.Errorf("editing to todo: %+v, %v", got, err)
	}
	title := "paint again"
	check(t, s.MarkTask(ctx, tk.ID, Finished))
	check(t, s.ArchiveTask(ctx, tk.ID, true))
	if got, err := s.UpdateTask(ctx, tk.ID, TaskEdit{Title: &title}); err != nil || !got.Archived {
		t.Errorf("renaming an archived task: %+v, %v", got, err)
	}
	check(t, s.ArchiveTask(ctx, tk.ID, false))
	if got, _ := s.GetTask(ctx, tk.ID); got.Archived {
		t.Error("unarchive did not take")
	}
}

// TestArchiveFinished puts every finished task in an active project
// away at once: done and dropped tasks, not open ones, not ones already
// archived, which do not count twice, and not ones in a shelved project.
func TestArchiveFinished(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	house := addProject(t, s, NewProject{Name: "house"})
	work := addProject(t, s, NewProject{Name: "work"})
	old := addProject(t, s, NewProject{Name: "old"})
	todo := addTask(t, s, NewTask{ProjectID: house.ID, Title: "paint"})
	done := addTask(t, s, NewTask{ProjectID: house.ID, Title: "fix the gate"})
	dropped := addTask(t, s, NewTask{ProjectID: work.ID, Title: "email accountant"})
	before := addTask(t, s, NewTask{ProjectID: work.ID, Title: "old thing"})
	shelved := addTask(t, s, NewTask{ProjectID: old.ID, Title: "in a shelved project"})
	check(t, s.MarkTask(ctx, done.ID, Finished))
	check(t, s.MarkTask(ctx, dropped.ID, Dropped))
	check(t, s.MarkTask(ctx, before.ID, Finished))
	check(t, s.ArchiveTask(ctx, before.ID, true))
	check(t, s.MarkTask(ctx, shelved.ID, Finished))
	check(t, s.MarkProject(ctx, old.ID, Shelved))

	if n, err := s.ArchiveFinished(ctx); err != nil || n != 2 {
		t.Fatalf("ArchiveFinished = %d, %v; want 2", n, err)
	}
	for _, c := range []struct {
		id       int64
		archived bool
	}{{todo.ID, false}, {done.ID, true}, {dropped.ID, true}, {before.ID, true}, {shelved.ID, false}} {
		if got, _ := s.GetTask(ctx, c.id); got.Archived != c.archived {
			t.Errorf("task %d archived = %v, want %v", c.id, got.Archived, c.archived)
		}
	}
	if n, err := s.ArchiveFinished(ctx); err != nil || n != 0 {
		t.Errorf("second ArchiveFinished = %d, %v; want 0", n, err)
	}
}

// TestMigrateRetiresDoing opens a database made while doing was a
// status and checks its doing tasks come back as todo, still open and
// still theirs, with the other statuses untouched.
func TestMigrateRetiresDoing(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(schema, "CHECK (status IN ('todo','done','dropped'))", "CHECK (status IN ('todo','doing','done','dropped'))", 1)
	if old == schema {
		t.Fatal("schema no longer has the status check this test widens")
	}
	if _, err := db.Exec(old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO projects (id, name, created_at) VALUES (1, 'p', '');
		INSERT INTO tasks (id, project_id, title, status, created_at) VALUES (1, 1, 'started', 'doing', ''), (2, 1, 'done one', 'done', ''), (3, 1, 'dropped one', 'dropped', '')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for id, want := range map[int64]Status{1: Todo, 2: Finished, 3: Dropped} {
		if got, err := s.GetTask(ctx, id); err != nil || got.Status != want {
			t.Errorf("task %d after migration: %+v, %v, want %s", id, got, err, want)
		}
	}
	open, err := s.ListTasks(ctx, TaskFilter{Open: true})
	if err != nil || len(open) != 1 || open[0].ID != 1 {
		t.Errorf("open tasks after migration = %+v, %v", open, err)
	}
}

// TestMigrateAddsArchived opens a database made before tasks could be
// archived and checks the column is added with nothing archived.
func TestMigrateAddsArchived(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(schema, "\tarchived   INTEGER NOT NULL DEFAULT 0,\n", "", 1)
	if old == schema {
		t.Fatal("schema no longer has the archived line this test removes")
	}
	if _, err := db.Exec(old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO projects (id, name, created_at) VALUES (1, 'p', '');
		INSERT INTO tasks (id, project_id, title, status, created_at) VALUES (1, 1, 'done one', 'done', ''), (2, 1, 'open one', 'todo', '')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tree, err := s.Tree(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree) != 1 || len(tree[0].Tasks) != 2 || tree[0].Tasks[0].Task.Archived {
		t.Errorf("tree after migration = %+v", tree)
	}
}

// TestMigrateAddsIssue opens a database made before tasks had issue links
// and checks the column is added with every task left without one.
func TestMigrateAddsIssue(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(schema, "\tissue      TEXT NOT NULL DEFAULT '',\n", "", 1)
	if old == schema {
		t.Fatal("schema no longer has the issue line this test removes")
	}
	if _, err := db.Exec(old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO projects (id, name, created_at) VALUES (1, 'p', '');
		INSERT INTO tasks (id, project_id, title, created_at) VALUES (1, 1, 'old one', '')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.GetTask(ctx, 1)
	if err != nil || got.Title != "old one" || got.Issue != "" {
		t.Errorf("task after migration = %+v, %v", got, err)
	}
	link := "owner/repo#1"
	if got, err := s.UpdateTask(ctx, 1, TaskEdit{Issue: &link}); err != nil || got.Issue != "https://github.com/owner/repo/issues/1" {
		t.Errorf("setting an issue after migration: %+v, %v", got, err)
	}
}

// TestMigrateAddsNotes opens a database made before tasks had notes and
// checks the column is added with every task left without any.
func TestMigrateAddsNotes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(schema, "\tnotes      TEXT NOT NULL DEFAULT '',\n", "", 1)
	if old == schema {
		t.Fatal("schema no longer has the notes line this test removes")
	}
	if _, err := db.Exec(old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO projects (id, name, created_at) VALUES (1, 'p', '');
		INSERT INTO tasks (id, project_id, title, created_at) VALUES (1, 1, 'old one', '')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.GetTask(ctx, 1)
	if err != nil || got.Title != "old one" || got.Notes != "" {
		t.Errorf("task after migration = %+v, %v", got, err)
	}
	notes := "two coats"
	if got, err := s.UpdateTask(ctx, 1, TaskEdit{Notes: &notes}); err != nil || got.Notes != "two coats" {
		t.Errorf("setting notes after migration: %+v, %v", got, err)
	}
}

func TestSubtasksAndCascade(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	p := addProject(t, s, NewProject{Name: "party"})
	task := addTask(t, s, NewTask{ProjectID: p.ID, Title: "invites"})
	a, err := s.AddSubtask(ctx, task.ID, " write list ")
	if err != nil || a.Title != "write list" || a.Done || a.TaskID != task.ID {
		t.Fatalf("AddSubtask = %+v, %v", a, err)
	}
	b := addSubtask(t, s, task.ID, "send")
	if _, err := s.AddSubtask(ctx, task.ID, ""); err == nil {
		t.Error("blank subtask accepted")
	}
	if _, err := s.AddSubtask(ctx, 99, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("subtask on missing task = %v", err)
	}
	if err := s.TickSubtask(ctx, a.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RenameSubtask(ctx, b.ID, "send them"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RenameSubtask(ctx, b.ID, " "); err == nil {
		t.Error("blank rename accepted")
	}
	subs, err := s.ListSubtasks(ctx, task.ID)
	if err != nil || len(subs) != 2 || !subs[0].Done || subs[1].Done || subs[1].Title != "send them" {
		t.Errorf("ListSubtasks = %+v, %v", subs, err)
	}
	// Ticking everything does not finish the task.
	check(t, s.TickSubtask(ctx, b.ID, true))
	if got, _ := s.GetTask(ctx, task.ID); got.Status != Todo {
		t.Errorf("task auto-closed: %s", got.Status)
	}
	if err := s.TickSubtask(ctx, a.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSubtask(ctx, a.ID); got.Done {
		t.Error("untick did not stick")
	}
	if err := s.DeleteSubtask(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSubtask(ctx, b.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete twice = %v", err)
	}
	if _, err := s.ListSubtasks(ctx, 99); !errors.Is(err, ErrNotFound) {
		t.Errorf("list for missing task = %v", err)
	}

	// Deleting the project takes its tasks and subtasks with it.
	if err := s.DeleteProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTask(ctx, task.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("task survived project delete: %v", err)
	}
	if _, err := s.GetSubtask(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("subtask survived project delete: %v", err)
	}
}

func TestTree(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	p := addProject(t, s, NewProject{Name: "a"})
	q := addProject(t, s, NewProject{Name: "b"})
	empty := addProject(t, s, NewProject{Name: "c"})
	check(t, s.MarkProject(ctx, q.ID, Done))
	t1 := addTask(t, s, NewTask{ProjectID: p.ID, Title: "one"})
	t2 := addTask(t, s, NewTask{ProjectID: p.ID, Title: "two"})
	check(t, s.MarkTask(ctx, t2.ID, Dropped))
	t3 := addTask(t, s, NewTask{ProjectID: p.ID, Title: "put away"})
	check(t, s.MarkTask(ctx, t3.ID, Finished))
	check(t, s.ArchiveTask(ctx, t3.ID, true))
	addTask(t, s, NewTask{ProjectID: q.ID, Title: "four"})
	addSubtask(t, s, t1.ID, "x")
	addSubtask(t, s, t1.ID, "y")
	addSubtask(t, s, t2.ID, "under the dropped task")
	addSubtask(t, s, t3.ID, "under the archived task")

	tree, err := s.Tree(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree) != 2 || tree[0].Project.ID != p.ID || tree[1].Project.ID != empty.ID {
		t.Fatalf("open tree projects = %+v", tree)
	}
	// The dropped task stays, greyed by the UI; the archived one goes.
	if len(tree[0].Tasks) != 2 || tree[0].Tasks[0].Task.ID != t1.ID || len(tree[0].Tasks[0].Subtasks) != 2 || tree[0].Tasks[1].Task.ID != t2.ID || len(tree[0].Tasks[1].Subtasks) != 1 {
		t.Errorf("open tree tasks = %+v", tree[0].Tasks)
	}
	if len(tree[1].Tasks) != 0 {
		t.Errorf("empty project has tasks: %+v", tree[1].Tasks)
	}

	all, err := s.Tree(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || len(all[0].Tasks) != 3 || len(all[1].Tasks) != 1 {
		t.Errorf("full tree = %+v", all)
	}
	if len(all[0].Tasks[2].Subtasks) != 1 {
		t.Errorf("subtask under the archived task missing from the full tree: %+v", all[0].Tasks[2])
	}
}

func TestOpenCreatesOwnerOnlyFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no unix modes on windows")
	}
	dir := filepath.Join(t.TempDir(), "data")
	path := filepath.Join(dir, "tasktracker.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for p, want := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode = %o, want %o", p, info.Mode().Perm(), want)
		}
	}
}

func TestOpenLeavesUserDirAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no unix modes on windows")
	}
	dir := t.TempDir()
	check(t, os.Chmod(dir, 0o755))
	s, err := Open(filepath.Join(dir, "tasktracker.db"))
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o755 {
		t.Errorf("user's dir mode changed to %o", info.Mode().Perm())
	}
	s, err = Open(filepath.Join(dir, "tasktracker.db"), OwnDir())
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("OwnDir did not tighten dir: %o", info.Mode().Perm())
	}
}

// TestDataVersion checks that the version moves on a change from another
// connection and stays put on this one's own.
func TestDataVersion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tasktracker.db")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	v0, err := a.DataVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	addProject(t, a, NewProject{Name: "mine"})
	if v, err := a.DataVersion(ctx); err != nil || v != v0 {
		t.Errorf("own write moved the version: %d -> %d, %v", v0, v, err)
	}
	addProject(t, b, NewProject{Name: "theirs"})
	v1, err := a.DataVersion(ctx)
	if err != nil || v1 == v0 {
		t.Errorf("another connection's write left the version: %d -> %d, %v", v0, v1, err)
	}
	if v, err := a.DataVersion(ctx); err != nil || v != v1 {
		t.Errorf("version moved with no write: %d -> %d, %v", v1, v, err)
	}
}

func TestSnapshotTo(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	addProject(t, s, NewProject{Name: "a"})
	out := filepath.Join(t.TempDir(), "snap.db")
	if err := s.SnapshotTo(ctx, out); err != nil {
		t.Fatal(err)
	}
	if err := s.SnapshotTo(ctx, out); err == nil {
		t.Error("overwrote an existing snapshot")
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(out); info.Mode().Perm() != 0o600 {
			t.Errorf("snapshot mode = %o, want 600", info.Mode().Perm())
		}
	}
	copy, err := Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	if ps, _ := copy.ListProjects(ctx, ProjectFilter{}); len(ps) != 1 {
		t.Errorf("snapshot has %d projects", len(ps))
	}
}

func TestOpenEscapesAwkwardPaths(t *testing.T) {
	ctx := context.Background()
	name := "a?b#c%20d e"
	if runtime.GOOS == "windows" {
		name = "a#b%20c d" // ? is not allowed in Windows paths
	}
	dir := filepath.Join(t.TempDir(), name)
	path := filepath.Join(dir, "tasktracker.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database not at the exact path: %v", err)
	}
	var fk int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Errorf("foreign_keys = %d, %v; the DSN parameters were lost", fk, err)
	}
	p := addProject(t, s, NewProject{Name: "x"})
	task := addTask(t, s, NewTask{ProjectID: p.ID, Title: "y"})
	check(t, s.DeleteProject(ctx, p.ID))
	if _, err := s.GetTask(ctx, task.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete did not cascade under an awkward path: %v", err)
	}
}
