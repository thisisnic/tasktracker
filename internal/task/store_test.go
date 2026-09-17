package task

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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
	p, err := s.AddProject(ctx, NewProject{Name: "  house ", Description: " fix it up ", GoalIDs: []int64{4, 2, 4}})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != 1 || p.Name != "house" || p.Description != "fix it up" || p.State != Active || !reflect.DeepEqual(p.GoalIDs, []int64{2, 4}) || p.CreatedAt.IsZero() {
		t.Errorf("added project = %+v", p)
	}
	if _, err := s.AddProject(ctx, NewProject{Name: " "}); err == nil {
		t.Error("blank name accepted")
	}
	if _, err := s.AddProject(ctx, NewProject{Name: "x", GoalIDs: []int64{0}}); err == nil {
		t.Error("goal id 0 accepted")
	}

	name, desc := "home", ""
	none := []int64{}
	p, err = s.UpdateProject(ctx, p.ID, ProjectEdit{Name: &name, Description: &desc, GoalIDs: &none})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "home" || p.Description != "" || p.GoalIDs != nil {
		t.Errorf("updated project = %+v", p)
	}
	goals := []int64{7}
	p, err = s.UpdateProject(ctx, p.ID, ProjectEdit{GoalIDs: &goals})
	if err != nil || !reflect.DeepEqual(p.GoalIDs, []int64{7}) {
		t.Errorf("goal edit = %+v, %v", p, err)
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
	if len(active) != 0 || len(all) != 1 || len(shelved) != 1 || shelved[0].State != Shelved || !reflect.DeepEqual(shelved[0].GoalIDs, []int64{7}) {
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
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM project_goals`).Scan(&n); err != nil || n != 0 {
		t.Errorf("goal links left behind: %d, %v", n, err)
	}
}

func TestTaskLifecycle(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	p := addProject(t, s, NewProject{Name: "house"})
	other := addProject(t, s, NewProject{Name: "work"})
	task, err := s.AddTask(ctx, NewTask{ProjectID: p.ID, Title: " paint the hall ", Due: "2026-10-01"})
	if err != nil {
		t.Fatal(err)
	}
	if task.ID != 1 || task.ProjectID != p.ID || task.Title != "paint the hall" || task.Status != Todo || task.Due != "2026-10-01" {
		t.Errorf("added task = %+v", task)
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

	title, due := "paint the hallway", ""
	task, err = s.UpdateTask(ctx, task.ID, TaskEdit{Title: &title, Due: &due, ProjectID: &other.ID})
	if err != nil {
		t.Fatal(err)
	}
	if task.Title != "paint the hallway" || task.Due != "" || task.ProjectID != other.ID {
		t.Errorf("updated task = %+v", task)
	}
	bad := int64(99)
	if _, err := s.UpdateTask(ctx, task.ID, TaskEdit{ProjectID: &bad}); !errors.Is(err, ErrNotFound) {
		t.Errorf("move to missing project = %v", err)
	}

	if err := s.MarkTask(ctx, task.ID, Doing); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkTask(ctx, task.ID, Status("blocked")); err == nil {
		t.Error("bad status accepted")
	}
	if got, _ := s.GetTask(ctx, task.ID); got.Status != Doing {
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
	p := addProject(t, s, NewProject{Name: "a", GoalIDs: []int64{1}})
	q := addProject(t, s, NewProject{Name: "b"})
	empty := addProject(t, s, NewProject{Name: "c"})
	check(t, s.MarkProject(ctx, q.ID, Done))
	t1 := addTask(t, s, NewTask{ProjectID: p.ID, Title: "one"})
	t2 := addTask(t, s, NewTask{ProjectID: p.ID, Title: "two"})
	check(t, s.MarkTask(ctx, t2.ID, Dropped))
	addTask(t, s, NewTask{ProjectID: q.ID, Title: "three"})
	addSubtask(t, s, t1.ID, "x")
	addSubtask(t, s, t1.ID, "y")
	addSubtask(t, s, t2.ID, "under the dropped task")

	tree, err := s.Tree(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree) != 2 || tree[0].Project.ID != p.ID || tree[1].Project.ID != empty.ID {
		t.Fatalf("open tree projects = %+v", tree)
	}
	if !reflect.DeepEqual(tree[0].Project.GoalIDs, []int64{1}) {
		t.Errorf("goal ids missing from tree: %+v", tree[0].Project)
	}
	if len(tree[0].Tasks) != 1 || tree[0].Tasks[0].Task.ID != t1.ID || len(tree[0].Tasks[0].Subtasks) != 2 {
		t.Errorf("open tree tasks = %+v", tree[0].Tasks)
	}
	if len(tree[1].Tasks) != 0 {
		t.Errorf("empty project has tasks: %+v", tree[1].Tasks)
	}

	all, err := s.Tree(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || len(all[0].Tasks) != 2 || len(all[1].Tasks) != 1 {
		t.Errorf("full tree = %+v", all)
	}
	if len(all[0].Tasks[1].Subtasks) != 1 {
		t.Errorf("subtask under the dropped task missing from the full tree: %+v", all[0].Tasks[1])
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
	dir := filepath.Join(t.TempDir(), "a?b#c%20d e")
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
