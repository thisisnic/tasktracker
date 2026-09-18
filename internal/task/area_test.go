package task

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func addArea(t *testing.T, s *Store, in NewArea) Area {
	t.Helper()
	a, err := s.AddArea(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAreaLifecycle(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	home := addArea(t, s, NewArea{Name: "  home "})
	garden := addArea(t, s, NewArea{Name: "garden", ParentID: home.ID})
	if home.Name != "home" || home.ParentID != 0 || garden.ParentID != home.ID {
		t.Errorf("added: %+v %+v", home, garden)
	}
	if _, err := s.AddArea(ctx, NewArea{Name: " "}); err == nil {
		t.Error("blank name accepted")
	}
	if _, err := s.AddArea(ctx, NewArea{Name: "x", ParentID: 99}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing parent: %v", err)
	}
	if _, err := s.GetArea(ctx, 99); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetArea(99): %v", err)
	}
	areas, err := s.ListAreas(ctx)
	if err != nil || len(areas) != 2 || areas[0].ID != home.ID || areas[1].ID != garden.ID {
		t.Fatalf("ListAreas = %+v, %v", areas, err)
	}
	if got := AreaPath(areas, garden.ID); got != "home / garden" {
		t.Errorf("AreaPath = %q", got)
	}
	if got := AreaPath(areas, 99); got != "" {
		t.Errorf("AreaPath(99) = %q", got)
	}

	name := "GARDEN"
	top := int64(0)
	garden, err = s.UpdateArea(ctx, garden.ID, AreaEdit{Name: &name, ParentID: &top})
	if err != nil || garden.Name != "GARDEN" || garden.ParentID != 0 {
		t.Errorf("UpdateArea = %+v, %v", garden, err)
	}
	// Moving home into GARDEN is fine; then GARDEN cannot go back into home.
	if _, err := s.UpdateArea(ctx, home.ID, AreaEdit{ParentID: &garden.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateArea(ctx, garden.ID, AreaEdit{ParentID: &home.ID}); err == nil || !strings.Contains(err.Error(), "inside itself") {
		t.Errorf("cycle accepted: %v", err)
	}
	if _, err := s.UpdateArea(ctx, garden.ID, AreaEdit{ParentID: &garden.ID}); err == nil {
		t.Error("self as parent accepted")
	}
	if _, err := s.UpdateArea(ctx, 99, AreaEdit{Name: &name}); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateArea(99): %v", err)
	}
}

func TestProjectsInAreas(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	home := addArea(t, s, NewArea{Name: "home"})
	garden := addArea(t, s, NewArea{Name: "garden", ParentID: home.ID})
	p := addProject(t, s, NewProject{Name: "grant report", AreaID: garden.ID})
	if p.AreaID != garden.ID {
		t.Errorf("AreaID = %d", p.AreaID)
	}
	if _, err := s.AddProject(ctx, NewProject{Name: "x", AreaID: 99}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing area: %v", err)
	}
	got, _ := s.GetProject(ctx, p.ID)
	if got.AreaID != garden.ID {
		t.Errorf("GetProject AreaID = %d", got.AreaID)
	}
	// An edit that says nothing about the area leaves it alone.
	name := "grant report 2026"
	got, err := s.UpdateProject(ctx, p.ID, ProjectEdit{Name: &name})
	if err != nil || got.AreaID != garden.ID {
		t.Errorf("after rename: %+v, %v", got, err)
	}
	none := int64(0)
	got, err = s.UpdateProject(ctx, p.ID, ProjectEdit{AreaID: &none})
	if err != nil || got.AreaID != 0 {
		t.Errorf("moved out: %+v, %v", got, err)
	}
	bad := int64(99)
	if _, err := s.UpdateProject(ctx, p.ID, ProjectEdit{AreaID: &bad}); !errors.Is(err, ErrNotFound) {
		t.Errorf("move to missing area: %v", err)
	}
}

func TestOutlineAndDeleteArea(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	home := addArea(t, s, NewArea{Name: "home"})
	garden := addArea(t, s, NewArea{Name: "garden", ParentID: home.ID})
	maint := addArea(t, s, NewArea{Name: "maintenance", ParentID: home.ID})
	report := addProject(t, s, NewProject{Name: "grant report", AreaID: garden.ID})
	addTask(t, s, NewTask{ProjectID: report.ID, Title: "draft"})
	done := addTask(t, s, NewTask{ProjectID: report.ID, Title: "outline"})
	check(t, s.MarkTask(ctx, done.ID, Finished))
	triage := addProject(t, s, NewProject{Name: "issue triage", AreaID: maint.ID})
	addTask(t, s, NewTask{ProjectID: triage.ID, Title: "weekly pass"})
	house := addProject(t, s, NewProject{Name: "house"})

	o, err := s.Outline(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Areas) != 1 || o.Areas[0].Area.ID != home.ID || len(o.Projects) != 1 || o.Projects[0].Project.ID != house.ID {
		t.Fatalf("top level: %+v", o)
	}
	top := o.Areas[0]
	if len(top.Areas) != 2 || top.Areas[0].Area.ID != garden.ID || top.Areas[1].Area.ID != maint.ID || len(top.Projects) != 0 {
		t.Errorf("home: %+v", top)
	}
	if n := top.OpenTasks(); n != 2 {
		t.Errorf("home open tasks = %d", n)
	}
	if as, ps := top.Counts(); as != 2 || ps != 2 {
		t.Errorf("home counts = %d areas, %d projects", as, ps)
	}
	node, ok := o.Find(garden.ID)
	if !ok || len(node.Projects) != 1 || node.Projects[0].Project.ID != report.ID || len(node.Projects[0].Tasks) != 1 {
		t.Errorf("Find(garden) = %+v, %v", node, ok)
	}
	if _, ok := o.Find(99); ok {
		t.Error("Find(99) found something")
	}

	// Deleting garden moves the report up into home.
	check(t, s.DeleteArea(ctx, garden.ID))
	if err := s.DeleteArea(ctx, garden.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	got, _ := s.GetProject(ctx, report.ID)
	if got.AreaID != home.ID {
		t.Errorf("report after delete: area %d", got.AreaID)
	}
	// Deleting home moves maintenance and the report to the top level.
	check(t, s.DeleteArea(ctx, home.ID))
	areas, _ := s.ListAreas(ctx)
	if len(areas) != 1 || areas[0].ID != maint.ID || areas[0].ParentID != 0 {
		t.Errorf("areas after delete: %+v", areas)
	}
	got, _ = s.GetProject(ctx, report.ID)
	if got.AreaID != 0 {
		t.Errorf("report after deleting home: area %d", got.AreaID)
	}
}

func TestNestIsSafeWithBrokenLinks(t *testing.T) {
	// A hand-edited database could hold a cycle or a dangling parent. Nest
	// shows everything rather than looping or dropping rows.
	areas := []Area{{ID: 1, Name: "a", ParentID: 2}, {ID: 2, Name: "b", ParentID: 1}, {ID: 3, Name: "c", ParentID: 9}}
	projects := []ProjectNode{{Project: Project{ID: 1, Name: "p", AreaID: 7}}}
	o := Nest(areas, projects)
	var names []string
	var walk func([]AreaNode)
	walk = func(nodes []AreaNode) {
		for _, n := range nodes {
			names = append(names, n.Area.Name)
			walk(n.Areas)
		}
	}
	walk(o.Areas)
	if !reflect.DeepEqual(names, []string{"c", "a", "b"}) {
		t.Errorf("areas = %v", names)
	}
	if len(o.Projects) != 1 {
		t.Errorf("project with a missing area not at the top: %+v", o.Projects)
	}
	if got := AreaPath(areas, 1); got != "b / a" {
		t.Errorf("AreaPath in a cycle = %q", got)
	}
}

func TestOpenMigratesProjectsWithoutAreas(t *testing.T) {
	// A database made before areas existed has no area_id column.
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE projects (
		id INTEGER PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL);
		INSERT INTO projects (name, created_at) VALUES ('house', '2026-01-01T00:00:00Z')`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	for range 2 { // opening twice must not try to add the column again
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		p, err := s.GetProject(ctx, 1)
		if err != nil || p.Name != "house" || p.AreaID != 0 {
			t.Fatalf("old project: %+v, %v", p, err)
		}
		a := addArea(t, s, NewArea{Name: "home"})
		if _, err := s.UpdateProject(ctx, 1, ProjectEdit{AreaID: &a.ID}); err != nil {
			t.Fatal(err)
		}
		check(t, s.DeleteArea(ctx, a.ID))
		s.Close()
	}
}
