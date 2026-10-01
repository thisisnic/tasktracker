package tui

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/thisisnic/tasktracker/internal/goallink"
	"github.com/thisisnic/tasktracker/internal/task"
	_ "modernc.org/sqlite"
)

// fixed is "today" for every test, so due-date words are stable.
var fixed = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

// setup builds a store with two projects, three tasks and two subtasks:
//
//	house (goal #3)
//	  paint the hall  due 2026-09-10 (overdue)   [x] buy paint  [ ] move furniture
//	  fix the gate    due 2026-09-20
//	work
//	  email accountant
func setup(t *testing.T, goals *goallink.Reader) (*model, *task.Store) {
	t.Helper()
	return setupAt(t, goals, filepath.Join(t.TempDir(), "tasktracker.db"))
}

// setupAt is setup with the database at path, for a test that opens a
// second connection to it as another process would.
func setupAt(t *testing.T, goals *goallink.Reader, path string) (*model, *task.Store) {
	t.Helper()
	store, err := task.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	house, err := store.AddProject(ctx, task.NewProject{Name: "house", Description: "fix it up", GoalIDs: []int64{3}})
	must(err)
	work, err := store.AddProject(ctx, task.NewProject{Name: "work"})
	must(err)
	paint, err := store.AddTask(ctx, task.NewTask{ProjectID: house.ID, Title: "paint the hall", Due: "2026-09-10"})
	must(err)
	_, err = store.AddTask(ctx, task.NewTask{ProjectID: house.ID, Title: "fix the gate", Due: "2026-09-20"})
	must(err)
	_, err = store.AddTask(ctx, task.NewTask{ProjectID: work.ID, Title: "email accountant"})
	must(err)
	buy, err := store.AddSubtask(ctx, paint.ID, "buy paint")
	must(err)
	_, err = store.AddSubtask(ctx, paint.ID, "move furniture")
	must(err)
	must(store.TickSubtask(ctx, buy.ID, true))

	m := newModel(ctx, store, goals)
	m.now = func() time.Time { return fixed }
	if err := m.reload(); err != nil {
		t.Fatal(err)
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m, store
}

// goaltrackerDB makes a database shaped like goaltracker's with goal #3.
func goaltrackerDB(t *testing.T) *goallink.Reader {
	t.Helper()
	path := filepath.Join(t.TempDir(), "goaltracker.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE goals (id INTEGER PRIMARY KEY, statement TEXT NOT NULL, period TEXT NOT NULL);
		INSERT INTO goals VALUES (3, 'finish the garden', '2026-Q3'), (5, 'run 500 km', '2026')`); err != nil {
		t.Fatal(err)
	}
	return goallink.New(path)
}

// send delivers msg to the model and then runs any command it returns,
// feeding the resulting messages back in, as the Bubble Tea runtime would.
// Batch and sequence messages are slices of commands and are unpacked. A
// budget caps how many commands run per delivery, since cursor blinks
// return batches that would otherwise fan out forever.
func send(m *model, msg tea.Msg, budget *int) {
	if msg == nil || *budget <= 0 {
		return
	}
	if rv := reflect.ValueOf(msg); rv.Kind() == reflect.Slice {
		var cmds []tea.Cmd
		for i := 0; i < rv.Len() && *budget > 0; i++ {
			if c, ok := rv.Index(i).Interface().(tea.Cmd); ok && c != nil {
				*budget--
				cmds = append(cmds, c)
			}
		}
		for _, out := range runCmds(cmds) {
			send(m, out, budget)
		}
		return
	}
	_, cmd := m.Update(msg)
	if cmd != nil && *budget > 0 {
		*budget--
		send(m, runCmd(cmd), budget)
	}
}

func deliver(m *model, msg tea.Msg) {
	budget := 16
	send(m, msg, &budget)
}

// cmdWait is how long a command gets to return before its message is
// dropped. Field moves return at once; cursor blinks and other timers do not.
const cmdWait = 100 * time.Millisecond

func runCmd(c tea.Cmd) tea.Msg {
	return runCmds([]tea.Cmd{c})[0]
}

// runCmds runs commands concurrently and returns their messages in order,
// with nil for any that did not finish within cmdWait.
func runCmds(cmds []tea.Cmd) []tea.Msg {
	out := make([]tea.Msg, len(cmds))
	chans := make([]chan tea.Msg, len(cmds))
	for i, c := range cmds {
		chans[i] = make(chan tea.Msg, 1)
		go func(c tea.Cmd, ch chan tea.Msg) { ch <- c() }(c, chans[i])
	}
	deadline := time.After(cmdWait)
	timedOut := false
	for i, ch := range chans {
		if timedOut {
			select {
			case out[i] = <-ch:
			default:
			}
			continue
		}
		select {
		case out[i] = <-ch:
		case <-deadline:
			timedOut = true
			select {
			case out[i] = <-ch:
			default:
			}
		}
	}
	return out
}

func press(m *model, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "space":
			msg = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
		case "backspace":
			msg = tea.KeyPressMsg{Code: tea.KeyBackspace}
		case "left":
			msg = tea.KeyPressMsg{Code: tea.KeyLeft}
		case "right":
			msg = tea.KeyPressMsg{Code: tea.KeyRight}
		case "shift+tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
		case "ctrl+j":
			msg = tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl}
		default:
			r := []rune(k)[0]
			msg = tea.KeyPressMsg{Code: r, Text: k}
		}
		deliver(m, msg)
	}
}

// typeText sends each rune of s as a key press.
func typeText(m *model, s string) {
	for _, r := range s {
		deliver(m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// plain renders the view without escape codes, so text split by styling
// can be matched as one string.
func plain(m *model) string { return ansi.Strip(m.View().Content) }

// foldProject folds project id as toggleFold would, with the stamp of its
// creation, without moving the cursor.
func foldProject(t *testing.T, m *model, store *task.Store, id int64) {
	t.Helper()
	p, err := store.GetProject(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	m.collapsed[target{rowProject, id}] = true
	m.foldMade[target{rowProject, id}] = stamp(p.CreatedAt)
}

// withNotes puts notes on task #1, paint the hall, and reloads.
func withNotes(t *testing.T, m *model, store *task.Store) {
	t.Helper()
	notes := "two coats"
	if _, err := store.UpdateTask(context.Background(), 1, task.TaskEdit{Notes: &notes}); err != nil {
		t.Fatal(err)
	}
	if err := m.reload(); err != nil {
		t.Fatal(err)
	}
}

// labels lists the rows as "kind:title" for comparing tree shapes.
func labels(m *model) []string {
	var out []string
	for _, r := range m.rows {
		switch r.kind {
		case rowArea:
			out = append(out, "A:"+r.area.Area.Name)
		case rowProject:
			out = append(out, "P:"+r.project.Project.Name)
		case rowTask:
			out = append(out, "T:"+r.task.Task.Title)
		case rowSubtask:
			out = append(out, "S:"+r.subtask.Title)
		case rowHeading:
			out = append(out, "H:"+r.bucket.String())
		}
	}
	return out
}

func TestTreeRowsAndView(t *testing.T) {
	m, _ := setup(t, nil)
	want := []string{"P:house", "T:paint the hall", "S:buy paint", "S:move furniture", "T:fix the gate", "P:work", "T:email accountant"}
	if got := labels(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v\nwant   %v", got, want)
	}
	view := plain(m)
	for _, want := range []string{"tasktracker · by project", "house", "2 open", "fix it up", "#3", "paint the hall", "1/2", "2026-09-10", "[x] buy paint", "[ ] move furniture"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q", want)
		}
	}
	press(m, "j")
	view = plain(m)
	for _, want := range []string{"status  todo", "7 days overdue", "subtasks 1/2"} {
		if !strings.Contains(view, want) {
			t.Errorf("task detail missing %q:\n%s", want, view)
		}
	}
	press(m, "j")
	if !strings.Contains(plain(m), "under   paint the hall") {
		t.Errorf("subtask detail:\n%s", plain(m))
	}
	press(m, "G")
	if r, _ := m.selected(); r.task.Task.Title != "email accountant" {
		t.Errorf("G went to %v", r.target())
	}
	press(m, "j") // clamps
	if m.cursor != len(m.rows)-1 {
		t.Error("cursor ran past the end")
	}
	press(m, "g")
	if m.cursor != 0 {
		t.Error("g did not go to the top")
	}
}

func TestGoalLabels(t *testing.T) {
	m, _ := setup(t, goaltrackerDB(t))
	if !strings.Contains(plain(m), "#3 finish the garden") {
		t.Errorf("goal statement not shown:\n%s", plain(m))
	}
	m2, _ := setup(t, goallink.New(filepath.Join(t.TempDir(), "absent.db")))
	view := plain(m2)
	if !strings.Contains(view, "#3") || strings.Contains(view, "finish the garden") || !strings.Contains(view, "not readable") {
		t.Errorf("missing goaltracker not handled:\n%s", view)
	}
}

func TestSpaceAdvances(t *testing.T) {
	m, store := setup(t, nil)
	ctx := context.Background()
	press(m, "j", "space")
	if tk, _ := store.GetTask(ctx, 1); tk.Status != task.Doing {
		t.Errorf("space on todo: %s", tk.Status)
	}
	if r, _ := m.selected(); r.target() != (target{rowTask, 1}) {
		t.Errorf("selection moved: %v", r.target())
	}
	press(m, "space")
	if tk, _ := store.GetTask(ctx, 1); tk.Status != task.Finished {
		t.Errorf("space on doing: %s", tk.Status)
	}
	// A done task stays in the tree until it is archived; the status line
	// says how.
	if got := labels(m); got[1] != "T:paint the hall" || !strings.Contains(m.status, "z archives it") {
		t.Errorf("after done: rows=%v status=%q", got, m.status)
	}
	press(m, "z")
	if got := labels(m); got[1] != "T:fix the gate" || !strings.Contains(m.status, "hidden; f shows archived") {
		t.Errorf("after archiving: rows=%v status=%q", got, m.status)
	}
	press(m, "f")
	if got := labels(m); got[1] != "T:paint the hall" || !m.showAll {
		t.Errorf("f did not show the archived task: %v", got)
	}
	press(m, "g", "j", "j", "space") // subtask row: toggles the tick
	if r, _ := m.selected(); r.kind != rowSubtask || r.subtask.Done {
		t.Errorf("space on a ticked subtask: %+v", r.subtask)
	}
	press(m, "space")
	if r, _ := m.selected(); !r.subtask.Done {
		t.Error("space did not tick the subtask back")
	}
	if tk, _ := store.GetTask(ctx, 1); tk.Status != task.Finished {
		t.Errorf("ticking changed the task: %s", tk.Status)
	}
	// A project leaving active is hidden with every task in it, so space
	// asks first; anything but y keeps it as it is.
	press(m, "g", "space")
	if m.mode != modeConfirmState || !strings.Contains(plain(m), `mark done project #1 "house" and hide it`) {
		t.Fatalf("space on project: mode=%v\n%s", m.mode, plain(m))
	}
	press(m, "n")
	if p, _ := store.GetProject(ctx, 1); p.State != task.Active || m.mode != modeBrowse || m.status != "kept" {
		t.Errorf("declined: state=%s mode=%v status=%q", p.State, m.mode, m.status)
	}
	press(m, "space", "y")
	if p, _ := store.GetProject(ctx, 1); p.State != task.Done {
		t.Errorf("confirmed: %s", p.State)
	}
	if r, _ := m.selected(); r.target() != (target{rowProject, 1}) || !strings.Contains(m.status, "project #1 done") {
		t.Errorf("after done: selected=%v status=%q", r.target(), m.status)
	}
	press(m, "space")
	if m.mode != modeConfirmState || !strings.Contains(plain(m), `shelve project #1 "house"`) {
		t.Errorf("space on done project: mode=%v", m.mode)
	}
	press(m, "y", "space") // shelved -> active goes straight through
	if p, _ := store.GetProject(ctx, 1); p.State != task.Active || m.mode != modeBrowse {
		t.Errorf("project did not cycle back to active: %s mode=%v", p.State, m.mode)
	}
}

func TestDropAndShelve(t *testing.T) {
	m, store := setup(t, nil)
	ctx := context.Background()
	press(m, "j", "x")
	if tk, _ := store.GetTask(ctx, 1); tk.Status != task.Dropped {
		t.Errorf("x on task: %s", tk.Status)
	}
	// A dropped task stays listed, greyed, until it is archived.
	if got := labels(m); got[1] != "T:paint the hall" || !strings.Contains(plain(m), "✗ paint the hall") || !strings.Contains(m.status, "z archives it") {
		t.Errorf("dropped task: rows=%v status=%q", got, m.status)
	}
	press(m, "f", "g", "j", "x") // x on a dropped task brings it back
	if tk, _ := store.GetTask(ctx, 1); tk.Status != task.Todo {
		t.Errorf("x on dropped task: %s", tk.Status)
	}
	press(m, "j", "x") // subtask: nothing to drop
	if !strings.Contains(m.status, "ticked with space") {
		t.Errorf("x on subtask: %q", m.status)
	}
	press(m, "g", "x")
	if p, _ := store.GetProject(ctx, 1); p.State != task.Shelved {
		t.Errorf("x on project: %s", p.State)
	}
	press(m, "f")
	if got := labels(m); got[0] != "P:work" {
		t.Errorf("shelved project still listed: %v", got)
	}
}

func TestArchive(t *testing.T) {
	m, store := setup(t, nil)
	ctx := context.Background()
	press(m, "z") // on a project
	if !strings.Contains(m.status, "only tasks are archived") {
		t.Errorf("z on project: %q", m.status)
	}
	press(m, "j", "z") // on an open task
	if tk, _ := store.GetTask(ctx, 1); tk.Archived || !strings.Contains(m.status, "still todo") {
		t.Errorf("z on open task: archived=%v status=%q", tk.Archived, m.status)
	}
	press(m, "j", "z") // on a subtask
	if !strings.Contains(m.status, "z on the task") {
		t.Errorf("z on subtask: %q", m.status)
	}
	press(m, "k", "space", "space", "z") // todo -> doing -> done, then archive
	if tk, _ := store.GetTask(ctx, 1); !tk.Archived {
		t.Fatal("z did not archive the done task")
	}
	if got := labels(m); !reflect.DeepEqual(got, []string{"P:house", "T:fix the gate", "P:work", "T:email accountant"}) {
		t.Errorf("rows after archiving: %v", got)
	}
	if r, _ := m.selected(); r.target() != (target{rowTask, 2}) {
		t.Errorf("cursor after archiving: %v", r.target())
	}
	// f shows it again, marked as archived in the row and the detail.
	press(m, "f", "g", "j")
	if r, _ := m.selected(); r.target() != (target{rowTask, 1}) {
		t.Fatalf("archived task not shown: %v", labels(m))
	}
	view := plain(m)
	for _, want := range []string{"1/2  archived  2026-09-10", "status  done · archived", "showing archived"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
	press(m, "z") // brings it back
	if tk, _ := store.GetTask(ctx, 1); tk.Archived || !strings.Contains(m.status, "back from the archive") {
		t.Errorf("z on archived task: archived=%v status=%q", tk.Archived, m.status)
	}
	press(m, "f")
	if got, view := labels(m), plain(m); got[1] != "T:paint the hall" || strings.Contains(view, "1/2  archived") || strings.Contains(view, "· archived") {
		t.Errorf("unarchived task hidden or still marked: %v\n%s", got, view)
	}
	// Reopening an archived task brings it back too, and the status line
	// says so; dropping an archived done task leaves it archived.
	press(m, "z", "f", "g", "j", "x") // archive, show, select, done -> dropped
	if tk, _ := store.GetTask(ctx, 1); !tk.Archived || tk.Status != task.Dropped || !strings.Contains(m.status, "still archived") {
		t.Errorf("x on an archived done task: %+v status=%q", tk, m.status)
	}
	press(m, "x") // dropped -> todo
	if tk, _ := store.GetTask(ctx, 1); tk.Archived || tk.Status != task.Todo || !strings.Contains(m.status, "back from the archive") {
		t.Errorf("x on an archived dropped task: %+v status=%q", tk, m.status)
	}
	press(m, "space", "space", "z", "g", "j", "space") // done, archive (still shown), done -> todo
	if tk, _ := store.GetTask(ctx, 1); tk.Archived || tk.Status != task.Todo || !strings.Contains(m.status, "back from the archive") {
		t.Errorf("space on an archived task: %+v status=%q", tk, m.status)
	}
	press(m, "f")
	if got := labels(m); got[1] != "T:paint the hall" {
		t.Errorf("reopened task hidden: %v", got)
	}
}

func TestDeadlineView(t *testing.T) {
	m, store := setup(t, nil)
	ctx := context.Background()
	press(m, "v")
	if m.view != viewDeadline || !strings.Contains(m.status, "by deadline") {
		t.Fatalf("v did not switch to the deadline view: view=%v status=%q", m.view, m.status)
	}
	// Every open task, soonest due first, undated last, subtasks under
	// their task, under a heading for how soon; the cursor is on the task
	// it was on.
	want := []string{"H:Overdue", "T:paint the hall", "S:buy paint", "S:move furniture", "H:Next 7 days", "T:fix the gate", "H:No deadline", "T:email accountant"}
	if got := labels(m); !reflect.DeepEqual(got, want) {
		t.Errorf("deadline rows = %v\nwant   %v", got, want)
	}
	press(m, "g")
	if r, ok := m.selected(); !ok || r.kind != rowHeading || r.bucket != bucketOverdue {
		t.Errorf("g: %d %v", m.cursor, r.target())
	}
	view := plain(m)
	for _, want := range []string{"tasktracker · by deadline", "paint the hall  house", "  [x] buy paint", "v by project", "▾ Overdue", "1 task", "▾ Next 7 days", "▾ No deadline", "before 2026-09-17", "tasks 1 open", "fold/unfold"} {
		if !strings.Contains(view, want) {
			t.Errorf("deadline view missing %q:\n%s", want, view)
		}
	}
	// Keys that act on a task say so on a heading.
	for _, k := range []string{"space", "x", "z", "d", "e"} {
		press(m, k)
		if m.mode != modeBrowse || !strings.Contains(m.status, "headings group tasks") {
			t.Errorf("%s on a heading: mode=%v status=%q", k, m.mode, m.status)
		}
	}
	// z on an open task says where it can be archived once finished,
	// since a finished task leaves this list.
	press(m, "j", "z") // paint the hall
	if tk, _ := store.GetTask(ctx, 1); tk.Archived || !strings.Contains(m.status, "then z in by project archives it") {
		t.Errorf("z on open task by deadline: archived=%v status=%q", tk.Archived, m.status)
	}
	press(m, "g", "a")
	if m.mode != modeBrowse || !strings.Contains(m.status, "select a task") {
		t.Errorf("a on a heading: mode=%v status=%q", m.mode, m.status)
	}
	// A heading is in no area, so a new project from one starts at the top.
	home, err := store.AddArea(ctx, task.NewArea{Name: "home"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateProject(ctx, 1, task.ProjectEdit{AreaID: &home.ID}); err != nil {
		t.Fatal(err)
	}
	press(m, "r", "g", "A")
	if pf, ok := m.form.(*projectForm); !ok || pf.area != 0 {
		t.Errorf("A on a heading: form=%T area=%v", m.form, m.form)
	}
	press(m, "esc", "j", "A") // paint the hall, in house, now in home
	if pf, ok := m.form.(*projectForm); !ok || pf.area != home.ID {
		t.Errorf("A on a task: form=%T", m.form)
	}
	press(m, "esc")
	none := int64(0)
	if _, err := store.UpdateProject(ctx, 1, task.ProjectEdit{AreaID: &none}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteArea(ctx, home.ID); err != nil {
		t.Fatal(err)
	}
	press(m, "r", "g")
	// A finished task is not due any more: it leaves this list, and the
	// status says where it went.
	press(m, "j", "space", "space")
	if got := labels(m); !reflect.DeepEqual(got, []string{"H:Next 7 days", "T:fix the gate", "H:No deadline", "T:email accountant"}) || !strings.Contains(m.status, "by project shows it until archived") {
		t.Errorf("after finishing: rows=%v status=%q", got, m.status)
	}
	if r, ok := m.selected(); !ok || r.task.Task.Title != "fix the gate" {
		t.Errorf("cursor after the task went: %d %v", m.cursor, r.target())
	}
	// v from a finished task, which has no row here, lands on the first
	// task listed from its project.
	press(m, "v", "g", "j") // paint the hall, done, in house
	if r, _ := m.selected(); r.target() != (target{rowTask, 1}) {
		t.Fatalf("tree cursor: %v", r.target())
	}
	press(m, "v")
	if r, _ := m.selected(); r.target() != (target{rowTask, 2}) {
		t.Errorf("v from a finished task: %v", r.target())
	}
	// f never brings finished tasks here; it adds open tasks in finished
	// projects, and the status says so.
	old, err := store.AddProject(ctx, task.NewProject{Name: "old"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddTask(ctx, task.NewTask{ProjectID: old.ID, Title: "leftover"}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkProject(ctx, old.ID, task.Done); err != nil {
		t.Fatal(err)
	}
	press(m, "f")
	if got := labels(m); !reflect.DeepEqual(got, []string{"H:Next 7 days", "T:fix the gate", "H:No deadline", "T:email accountant", "T:leftover"}) || !strings.Contains(m.status, "adds open tasks in finished projects") {
		t.Errorf("with archived shown: %v status=%q", got, m.status)
	}
	if strings.Contains(plain(m), "Overdue") {
		t.Errorf("a finished task listed with f:\n%s", plain(m))
	}
	press(m, "f")
	if got := labels(m); len(got) != 4 || !strings.Contains(m.status, "drops open tasks in finished projects") {
		t.Errorf("with archived hidden again: %v status=%q", got, m.status)
	}
	if err := store.DeleteProject(ctx, old.ID); err != nil {
		t.Fatal(err)
	}
	press(m, "r")
	// A date change moves a task; an undated one goes to the end.
	later := "2026-12-01"
	if _, err := store.UpdateTask(ctx, 2, task.TaskEdit{Due: &later}); err != nil {
		t.Fatal(err)
	}
	today := "2026-09-17"
	if _, err := store.UpdateTask(ctx, 3, task.TaskEdit{Due: &today}); err != nil {
		t.Fatal(err)
	}
	press(m, "r")
	if got := labels(m); !reflect.DeepEqual(got, []string{"H:Next 7 days", "T:email accountant", "H:Longer", "T:fix the gate"}) {
		t.Errorf("after date changes: %v", got)
	}
	// Saving by deadline leaves the tree's folds alone: fold house first.
	foldProject(t, m, store, 1)
	press(m, "G", "a") // a adds under the selected task's project
	if m.mode != modeForm {
		t.Fatal("a did not open the form in the deadline view")
	}
	typeText(m, "sooner")
	press(m, "enter", "enter", "enter", "enter", "enter") // due, issue, notes, project, submit
	if m.mode != modeBrowse || m.err != nil {
		t.Fatalf("form: mode=%v err=%v", m.mode, m.err)
	}
	if r, _ := m.selected(); r.kind != rowTask || r.task.Task.Title != "sooner" || r.project.Project.Name != "house" || m.cursor != len(m.rows)-1 || labels(m)[m.cursor-1] != "H:No deadline" {
		t.Errorf("new undated task: %v at %d of %d", r.target(), m.cursor, len(m.rows))
	}
	if !m.collapsed[target{rowProject, 1}] {
		t.Error("saving by deadline unfolded house in the tree")
	}
	press(m, "v")
	if got := labels(m); !reflect.DeepEqual(got, []string{"P:house", "P:work", "T:email accountant"}) {
		t.Errorf("tree after saving by deadline: %v", got)
	}
	// The selected task is folded away, so the cursor lands on its project.
	if r, _ := m.selected(); r.target() != (target{rowProject, 1}) {
		t.Errorf("cursor after v onto a folded project: %v", r.target())
	}
	press(m, "v")
	delete(m.collapsed, target{rowProject, 1})
	for _, id := range []int64{2, 3, 4} {
		if err := store.DeleteTask(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	press(m, "r")
	if len(m.rows) != 0 || !strings.Contains(plain(m), "no open tasks") {
		t.Errorf("empty deadline view: rows=%d\n%s", len(m.rows), plain(m))
	}
	press(m, "a")
	if m.mode != modeBrowse || !strings.Contains(m.status, "v goes back") {
		t.Errorf("a with nothing listed: mode=%v status=%q", m.mode, m.status)
	}
	press(m, "v")
	if m.view != viewTree || len(m.rows) == 0 || m.status != "by project" {
		t.Errorf("v did not go back: view=%v rows=%d status=%q", m.view, len(m.rows), m.status)
	}
}

// TestDeadlineFold folds headings with the arrows, as areas fold in the
// tree: on a heading, or on a task under it. Folds last across switches
// between the views, and saving a task under a folded heading opens it.
func TestDeadlineFold(t *testing.T) {
	m, store := setup(t, nil)
	ctx := context.Background()
	press(m, "v", "g", "left") // on Overdue
	if got := labels(m); !reflect.DeepEqual(got, []string{"H:Overdue", "H:Next 7 days", "T:fix the gate", "H:No deadline", "T:email accountant"}) || m.status != "collapsed Overdue; ← shows its tasks again" {
		t.Errorf("fold on a heading: rows=%v status=%q", got, m.status)
	}
	view := plain(m)
	if !strings.Contains(view, "▸ Overdue") || !strings.Contains(view, "collapsed; ← shows its tasks") {
		t.Errorf("folded heading view:\n%s", view)
	}
	press(m, "j", "j", "right") // on fix the gate: folds Next 7 days and moves there
	if got := labels(m); !reflect.DeepEqual(got, []string{"H:Overdue", "H:Next 7 days", "H:No deadline", "T:email accountant"}) || m.cursor != 1 {
		t.Errorf("fold from a task: rows=%v cursor=%d", got, m.cursor)
	}
	// Folds survive a switch to by project and back, and are not pruned.
	// v from a heading lands on the first task in the tree due about as
	// soon; v back from that task, folded away here, lands on the heading.
	press(m, "v")
	if r, _ := m.selected(); r.target() != (target{rowTask, 2}) {
		t.Errorf("v from the Next 7 days heading: %v", r.target())
	}
	press(m, "v")
	if got := labels(m); !reflect.DeepEqual(got, []string{"H:Overdue", "H:Next 7 days", "H:No deadline", "T:email accountant"}) {
		t.Errorf("after v v: %v", got)
	}
	if r, _ := m.selected(); r.target() != (target{rowHeading, int64(bucketWeek)}) {
		t.Errorf("v back onto a folded heading: %v", r.target())
	}
	// v from a heading lands on its first task in the tree, or on that
	// task's project when the task is folded away there.
	press(m, "g", "v")
	if r, _ := m.selected(); r.target() != (target{rowTask, 1}) {
		t.Errorf("v from Overdue: %v", r.target())
	}
	press(m, "v")
	foldProject(t, m, store, 1)
	press(m, "g", "v")
	if r, _ := m.selected(); r.target() != (target{rowProject, 1}) {
		t.Errorf("v from Overdue with its task folded away: %d %v", m.cursor, r.target())
	}
	delete(m.collapsed, target{rowProject, 1})
	press(m, "v")
	if err := store.MarkTask(ctx, 1, task.Finished); err != nil {
		t.Fatal(err)
	}
	press(m, "r", "g")
	if r, _ := m.selected(); r.kind != rowHeading || r.bucket != bucketWeek {
		t.Fatalf("Overdue should be gone: %v", labels(m))
	}
	if err := store.MarkTask(ctx, 1, task.Todo); err != nil {
		t.Fatal(err)
	}
	press(m, "r", "g")
	// f says this list has open tasks only.
	press(m, "f")
	if !strings.Contains(m.status, "adds open tasks in finished projects") {
		t.Errorf("f by deadline: %q", m.status)
	}
	press(m, "f")
	// Saving a task that lands under a folded heading opens the heading.
	press(m, "G", "e") // email accountant: give it a date within the week
	press(m, "enter")
	typeText(m, "tomorrow")
	press(m, "enter", "enter", "enter", "enter", "enter") // issue, notes, status, project, submit
	if m.mode != modeBrowse || m.err != nil {
		t.Fatalf("form: mode=%v err=%v", m.mode, m.err)
	}
	if got := labels(m); !reflect.DeepEqual(got, []string{"H:Overdue", "H:Next 7 days", "T:email accountant", "T:fix the gate"}) {
		t.Errorf("after saving under a folded heading: %v", got)
	}
	if r, _ := m.selected(); r.target() != (target{rowTask, 3}) {
		t.Errorf("cursor after save: %v", r.target())
	}
	// Unfolding a heading shows its tasks again. A fold on a heading that
	// has since emptied is kept, not pruned: the heading comes back folded
	// when a task lands in its bucket again, and can be undone then.
	press(m, "g", "left")
	if got := labels(m); got[1] != "T:paint the hall" || m.status != "expanded Overdue" {
		t.Errorf("unfold: rows=%v status=%q", got, m.status)
	}
	press(m, "left")
	if err := store.MarkTask(ctx, 1, task.Finished); err != nil {
		t.Fatal(err)
	}
	press(m, "r")
	if got := labels(m); got[0] != "H:Next 7 days" || !m.collapsed[target{rowHeading, int64(bucketOverdue)}] {
		t.Errorf("empty bucket kept its heading or lost its fold: %v", got)
	}
	press(m, "v", "v")
	if !m.collapsed[target{rowHeading, int64(bucketOverdue)}] {
		t.Error("a heading fold was pruned by a reload")
	}
	if err := store.MarkTask(ctx, 1, task.Todo); err != nil {
		t.Fatal(err)
	}
	press(m, "r", "g")
	if got := labels(m); got[0] != "H:Overdue" || got[1] != "H:Next 7 days" || !strings.Contains(plain(m), "▸ Overdue") {
		t.Errorf("heading back after its bucket refilled: %v", got)
	}
	press(m, "left")
	if got := labels(m); got[1] != "T:paint the hall" || m.status != "expanded Overdue" {
		t.Errorf("unfold after refilling: rows=%v status=%q", got, m.status)
	}
}

// TestLandByDeadlineFolded switches to by deadline from a project whose
// only open task is under a folded heading: the cursor lands on that
// heading rather than at the top.
func TestLandByDeadlineFolded(t *testing.T) {
	m, store := setup(t, nil)
	m.collapsed[target{rowHeading, int64(bucketNone)}] = true
	m.selectTarget(target{rowProject, 2}) // work: email accountant, undated
	press(m, "v")
	if r, _ := m.selected(); r.target() != (target{rowHeading, int64(bucketNone)}) {
		t.Errorf("v from work with No deadline folded: %d %v", m.cursor, r.target())
	}
	press(m, "v")
	m.selectTarget(target{rowProject, 1}) // house: paint the hall is listed under Overdue
	press(m, "v")
	if r, _ := m.selected(); r.target() != (target{rowTask, 1}) {
		t.Errorf("v from house: %v", r.target())
	}
	// From a project the landing is its soonest due task, whatever the
	// tree order: work lists email accountant (undated) before urgent.
	delete(m.collapsed, target{rowHeading, int64(bucketNone)})
	urgent, err := store.AddTask(context.Background(), task.NewTask{ProjectID: 2, Title: "urgent", Due: "2026-09-01"})
	if err != nil {
		t.Fatal(err)
	}
	press(m, "v", "r")
	m.selectTarget(target{rowProject, 2})
	press(m, "v")
	if r, _ := m.selected(); r.target() != (target{rowTask, urgent.ID}) {
		t.Errorf("v from work: %v, want its overdue task", r.target())
	}
	// With both of work's headings folded, the landing is the heading of
	// its soonest due task, again whatever the tree order.
	m.collapsed[target{rowHeading, int64(bucketNone)}] = true
	m.collapsed[target{rowHeading, int64(bucketOverdue)}] = true
	press(m, "v")
	m.selectTarget(target{rowProject, 2})
	press(m, "v")
	if r, _ := m.selected(); r.target() != (target{rowHeading, int64(bucketOverdue)}) {
		t.Errorf("v from work with its headings folded: %v %v", r.target(), labels(m))
	}
	delete(m.collapsed, target{rowHeading, int64(bucketNone)})
	delete(m.collapsed, target{rowHeading, int64(bucketOverdue)})
	press(m, "v", "v", "g", "j") // back on urgent
	// Finishing a task whose group is followed by a heading leaves the
	// cursor on the next task, not the heading.
	press(m, "j") // paint the hall: last group under Overdue, before Next 7 days
	if r, _ := m.selected(); r.target() != (target{rowTask, 1}) {
		t.Fatalf("cursor before finishing: %d %v %v", m.cursor, r.target(), labels(m))
	}
	press(m, "space", "space")
	if r, _ := m.selected(); r.target() != (target{rowTask, 2}) || labels(m)[m.cursor-1] != "H:Next 7 days" {
		t.Errorf("cursor after finishing before a heading: %d %v %v", m.cursor, r.target(), labels(m))
	}
}

func TestDeleteFlow(t *testing.T) {
	m, store := setup(t, nil)
	ctx := context.Background()
	press(m, "j", "j", "d")
	if m.mode != modeConfirmDelete || !strings.Contains(plain(m), `delete subtask #1 "buy paint"`) {
		t.Fatalf("d on subtask: mode=%v", m.mode)
	}
	press(m, "n")
	if m.mode != modeBrowse || m.status != "kept" {
		t.Errorf("declined: mode=%v status=%q", m.mode, m.status)
	}
	press(m, "d", "y")
	if _, err := store.GetSubtask(ctx, 1); err == nil {
		t.Error("subtask not deleted")
	}
	if r, _ := m.selected(); r.kind != rowSubtask || r.subtask.Title != "move furniture" {
		t.Errorf("cursor after delete: %v", r.target())
	}
	press(m, "k", "d", "y") // the task, with its remaining subtask
	if _, err := store.GetTask(ctx, 1); err == nil {
		t.Error("task not deleted")
	}
	press(m, "g", "d", "y") // the project and everything in it
	if got := labels(m); !reflect.DeepEqual(got, []string{"P:work", "T:email accountant"}) {
		t.Errorf("after deleting house: %v", got)
	}
	press(m, "d", "y")
	if len(m.rows) != 0 || m.err != nil || !strings.Contains(plain(m), "no projects yet") {
		t.Errorf("empty state: rows=%d err=%v", len(m.rows), m.err)
	}
	press(m, "a")
	if m.mode != modeBrowse || !strings.Contains(m.status, "press A") {
		t.Errorf("a with no projects: mode=%v status=%q", m.mode, m.status)
	}
}

func TestDeleteFailureStaysVisible(t *testing.T) {
	m, store := setup(t, nil)
	press(m, "j")
	if err := store.DeleteTask(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	press(m, "d", "y")
	if m.err == nil || m.mode != modeBrowse || !strings.Contains(plain(m), "error:") {
		t.Errorf("failed delete: err=%v mode=%v", m.err, m.mode)
	}
}

func TestAddTaskViaForm(t *testing.T) {
	m, store := setup(t, nil)
	press(m, "G", "a") // under work
	if m.mode != modeForm {
		t.Fatal("a did not open the form")
	}
	typeText(m, "send invoice")
	press(m, "enter") // task -> due
	typeText(m, "tomorrow")
	press(m, "enter") // due -> issue
	typeText(m, "not an issue")
	press(m, "enter") // refused: stays on issue
	if m.mode != modeForm {
		t.Fatalf("bad issue accepted: mode=%v", m.mode)
	}
	for range len("not an issue") {
		press(m, "backspace")
	}
	typeText(m, "owner/repo#42")
	press(m, "enter") // issue -> notes
	if !strings.Contains(plain(m), "ctrl+j new line in notes") {
		t.Errorf("task form help line:\n%s", plain(m))
	}
	typeText(m, "ask for the PO number")
	press(m, "ctrl+j") // a new line, not the next field
	typeText(m, "then send")
	press(m, "enter") // notes -> project (prefilled: work)
	press(m, "enter") // submit
	if m.mode != modeBrowse || m.err != nil {
		t.Fatalf("form did not close cleanly: mode=%v err=%v", m.mode, m.err)
	}
	tasks, _ := store.ListTasks(context.Background(), task.TaskFilter{ProjectID: 2})
	if len(tasks) != 2 || tasks[1].Title != "send invoice" || tasks[1].Due != "2026-09-18" || tasks[1].Issue != "https://github.com/owner/repo/issues/42" || tasks[1].Notes != "ask for the PO number\nthen send" {
		t.Errorf("saved task: %+v", tasks)
	}
	// The row marks that there are notes; the detail pane shows them
	// under their heading.
	if !strings.Contains(plain(m), "send invoice") || !strings.Contains(plain(m), "≡  2026-09-18") {
		t.Errorf("row not marked:\n%s", plain(m))
	}
	detail := ansi.Strip(m.viewDetail(40))
	if i := strings.Index(detail, "\nnotes\n"); i < 0 || !strings.HasPrefix(detail[i+len("\nnotes\n"):], "ask for the PO number ") || !strings.Contains(detail[i:], "\nthen send ") {
		t.Errorf("detail pane missing the notes:\n%s", detail)
	}
	if r, _ := m.selected(); r.target() != (target{rowTask, tasks[1].ID}) {
		t.Errorf("cursor not on the new task: %v", r.target())
	}
	if !strings.Contains(plain(m), "tomorrow") {
		t.Error("due words not shown")
	}
	// The detail pane shows the link short, as a terminal hyperlink.
	if !strings.Contains(plain(m), "issue   owner/repo#42") {
		t.Errorf("issue not in the detail pane:\n%s", plain(m))
	}
	if !strings.Contains(m.View().Content, "https://github.com/owner/repo/issues/42") {
		t.Error("issue ref is not a hyperlink to the URL")
	}
}

func TestFormValidationAndCancel(t *testing.T) {
	m, store := setup(t, nil)
	press(m, "a", "enter") // empty title must not advance
	typeText(m, "x")
	press(m, "enter")
	typeText(m, "soon")
	press(m, "enter") // bad due must not advance
	press(m, "esc")
	if m.mode != modeBrowse || m.status != "cancelled" {
		t.Errorf("esc: mode=%v status=%q", m.mode, m.status)
	}
	tasks, _ := store.ListTasks(context.Background(), task.TaskFilter{})
	if len(tasks) != 3 {
		t.Errorf("cancelled form saved something: %d tasks", len(tasks))
	}
}

func TestFormStaysOpenWhenSaveFails(t *testing.T) {
	m, store := setup(t, nil)
	press(m, "j", "e")
	// Pull the task out from under the form so the save fails.
	if err := store.DeleteTask(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	typeText(m, " again")
	press(m, "enter", "enter", "enter", "enter", "enter", "enter") // due, issue, notes, status, project, submit
	if m.mode != modeForm || m.err == nil {
		t.Fatalf("failed save: mode=%v err=%v", m.mode, m.err)
	}
	if tf, ok := m.form.(*taskForm); !ok || tf.title != "paint the hall again" {
		t.Errorf("typed text lost: %+v", m.form)
	}
	if !strings.Contains(plain(m), "error:") {
		t.Error("error not shown while the form is open")
	}
	press(m, "esc")
	if m.mode != modeBrowse {
		t.Error("esc did not close the failed form")
	}
}

// TestDeadlineOrder checks that tasks due the same day, and undated
// tasks, keep their by-project order, across nested areas.
func TestDeadlineOrder(t *testing.T) {
	m, store := setup(t, nil)
	ctx := context.Background()
	home, err := store.AddArea(ctx, task.NewArea{Name: "home"})
	if err != nil {
		t.Fatal(err)
	}
	garden, err := store.AddArea(ctx, task.NewArea{Name: "garden", ParentID: home.ID})
	if err != nil {
		t.Fatal(err)
	}
	beds, err := store.AddProject(ctx, task.NewProject{Name: "beds", AreaID: garden.ID})
	if err != nil {
		t.Fatal(err)
	}
	shed, err := store.AddProject(ctx, task.NewProject{Name: "shed", AreaID: home.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []task.NewTask{
		{ProjectID: beds.ID, Title: "weed", Due: "2026-09-20"}, // same day as fix the gate
		{ProjectID: beds.ID, Title: "mulch"},                   // undated
		{ProjectID: shed.ID, Title: "clear out", Due: "2026-09-20"},
		{ProjectID: shed.ID, Title: "paint shed"},
	} {
		if _, err := store.AddTask(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	press(m, "r")
	tree := labels(m)
	press(m, "v")
	// By project, areas come before top-level projects and garden (inside
	// home) before shed; that order holds within a due date and among the
	// undated.
	want := []string{
		"H:Overdue", "T:paint the hall", "S:buy paint", "S:move furniture", // 2026-09-10
		"H:Next 7 days", "T:weed", "T:clear out", "T:fix the gate", // 2026-09-20, tree order
		"H:No deadline", "T:mulch", "T:paint shed", "T:email accountant", // undated, tree order
	}
	if got := labels(m); !reflect.DeepEqual(got, want) {
		t.Errorf("deadline rows = %v\nwant   %v\ntree   %v", got, want, tree)
	}
	// v from an area or project lands on the first task inside it, or at
	// the top when it holds none.
	press(m, "v")
	m.selectTarget(target{rowArea, home.ID})
	press(m, "v")
	if r, _ := m.selected(); r.task.Task.Title != "weed" {
		t.Errorf("v from home: %v", r.target())
	}
	press(m, "v")
	m.selectTarget(target{rowProject, 2}) // work
	press(m, "v")
	if r, _ := m.selected(); r.task.Task.Title != "email accountant" {
		t.Errorf("v from work: %v", r.target())
	}
	press(m, "v")
	empty, err := store.AddProject(ctx, task.NewProject{Name: "empty"})
	if err != nil {
		t.Fatal(err)
	}
	press(m, "r")
	m.selectTarget(target{rowProject, empty.ID})
	press(m, "v")
	if m.cursor != 0 {
		t.Errorf("v from an empty project: cursor %d, want the top", m.cursor)
	}
	press(m, "A") // a project saved by deadline has no row here
	typeText(m, "fence")
	press(m, "enter", "enter", "enter", "enter")
	if m.mode != modeBrowse || m.err != nil || !strings.Contains(m.status, "saved project") || !strings.Contains(m.status, "v shows it by project") {
		t.Errorf("A by deadline: mode=%v err=%v status=%q", m.mode, m.err, m.status)
	}
}

func TestBucketOf(t *testing.T) {
	cases := map[string]bucket{
		"":           bucketNone,
		"2026-09-16": bucketOverdue,
		"2026-09-17": bucketWeek,
		"2026-09-23": bucketWeek,
		"2026-09-24": bucketMonth,
		"2026-10-16": bucketMonth,
		"2026-10-17": bucketLonger,
		"2027-01-01": bucketLonger,
	}
	for due, want := range cases {
		if got := bucketOf(due, fixed); got != want {
			t.Errorf("bucketOf(%q) = %v, want %v", due, got, want)
		}
	}
	spans := map[bucket]string{
		bucketOverdue: "before 2026-09-17",
		bucketWeek:    "2026-09-17 to 2026-09-23",
		bucketMonth:   "2026-09-24 to 2026-10-16",
		bucketLonger:  "from 2026-10-17",
		bucketNone:    "no due date",
	}
	for b, want := range spans {
		if got := b.span(fixed); got != want {
			t.Errorf("%v span = %q, want %q", b, got, want)
		}
	}
}

func TestDeadlineViewRowStyling(t *testing.T) {
	m, _ := setup(t, nil)
	press(m, "v", "j", "j", "j", "j") // fix the gate selected, past its heading; paint the hall is overdue and unselected
	view := m.View().Content
	lines := strings.Split(view, "\n")
	var overdue, selected string
	for _, l := range lines {
		if strings.Contains(l, "paint the hall") {
			overdue = l
		}
		if strings.Contains(l, "fix the gate") {
			selected = l
		}
	}
	if overdue == "" || selected == "" {
		t.Fatalf("rows not found:\n%s", view)
	}
	// The selected row is one reverse-video run: no reset before the end.
	start := strings.Index(selected, "\x1b[7m")
	if start < 0 {
		t.Fatalf("selected row not highlighted: %q", selected)
	}
	inner := selected[start+4:]
	if i := strings.Index(inner, "\x1b[m"); i < 0 || strings.Contains(inner[:i], "\x1b[") {
		t.Errorf("selected row is not one highlight run: %q", selected)
	}
	if !strings.Contains(overdue, overdueStyle.Render("2026-09-10")) {
		t.Errorf("overdue date not red: %q", overdue)
	}
	if !strings.Contains(overdue, dimStyle.Render("  house")) {
		t.Errorf("project name not dimmed: %q", overdue)
	}
	// Narrow enough that the project name is cut short: what is left of it
	// is still dimmed, and the title is not.
	m.Update(tea.WindowSizeMsg{Width: 73, Height: 30})
	overdue = ""
	for _, l := range strings.Split(m.View().Content, "\n") {
		if strings.Contains(l, "paint the hall") {
			overdue = l
		}
	}
	if !strings.Contains(overdue, "paint the hall"+dimStyle.Render("  ho…")) {
		t.Errorf("cut project name not dimmed: %q", overdue)
	}
}

func TestCopyTaskViaForm(t *testing.T) {
	m, store := setup(t, nil)
	ctx := context.Background()
	issue, notes := "owner/repo#1", "two coats"
	if _, err := store.UpdateTask(ctx, 1, task.TaskEdit{Issue: &issue, Notes: &notes}); err != nil {
		t.Fatal(err)
	}
	press(m, "r")
	press(m, "c") // on a project
	if m.mode != modeBrowse || !strings.Contains(m.status, "select a task") {
		t.Errorf("c on project: mode=%v status=%q", m.mode, m.status)
	}
	press(m, "j", "j", "c") // on a subtask: copies the task it is under
	if m.mode != modeForm {
		t.Fatal("c did not open the form")
	}
	if !strings.Contains(plain(m), "copied from #1 (with its 2 subtasks)") {
		t.Errorf("copy form title:\n%s", plain(m))
	}
	tf, ok := m.form.(*taskForm)
	// The original's issue is not the copy's: the field starts blank.
	if !ok || tf.copyFrom != 1 || tf.title != "paint the hall" || tf.due != "2026-09-10" || tf.issue != "" || tf.notes != "two coats" || tf.project != 1 {
		t.Fatalf("copy form not filled in: %+v", m.form)
	}
	typeText(m, " upstairs")
	press(m, "enter") // title -> due
	for range len("2026-09-10") {
		press(m, "backspace")
	}
	typeText(m, "tomorrow")
	press(m, "enter")      // due -> issue (blank)
	press(m, "enter")      // issue -> notes (the original's)
	press(m, "enter")      // notes -> project
	press(m, "j", "enter") // house -> work, submit
	if m.mode != modeBrowse || m.err != nil {
		t.Fatalf("form: mode=%v err=%v", m.mode, m.err)
	}
	if !strings.Contains(m.status, "copied task #1 to task #4 with its 2 subtasks, unticked") {
		t.Errorf("status: %q", m.status)
	}
	got, err := store.GetTask(ctx, 4)
	if err != nil || got.Title != "paint the hall upstairs" || got.Due != "2026-09-18" || got.Issue != "" || got.ProjectID != 2 || got.Status != task.Todo {
		t.Errorf("the copy: %+v, %v", got, err)
	}
	subs, _ := store.ListSubtasks(ctx, 4)
	if len(subs) != 2 || subs[0].Title != "buy paint" || subs[0].Done {
		t.Errorf("copied subtasks: %+v", subs)
	}
	if orig, _ := store.GetTask(ctx, 1); orig.Title != "paint the hall" {
		t.Errorf("original changed: %+v", orig)
	}
	if r, _ := m.selected(); r.target() != (target{rowTask, 4}) {
		t.Errorf("cursor not on the copy: %v", r.target())
	}
	if got := labels(m); !reflect.DeepEqual(got, []string{"P:house", "T:paint the hall", "S:buy paint", "S:move furniture", "T:fix the gate", "P:work", "T:email accountant", "T:paint the hall upstairs", "S:buy paint", "S:move furniture"}) {
		t.Errorf("rows after copying: %v", got)
	}
	// A task with no subtasks says so in the title and the status.
	press(m, "G", "k", "k", "k", "c") // email accountant
	if !strings.Contains(plain(m), "copied from #3\n") && !strings.Contains(plain(m), "copied from #3 ") {
		t.Errorf("copy form title for a task with no subtasks:\n%s", plain(m))
	}
	press(m, "enter", "enter", "enter", "enter", "enter") // due, issue, notes, project, submit
	if m.mode != modeBrowse || m.err != nil || m.status != "copied task #3 to task #5" {
		t.Errorf("plain copy: mode=%v err=%v status=%q", m.mode, m.err, m.status)
	}
}

func TestEditTaskViaForm(t *testing.T) {
	m, store := setup(t, nil)
	withNotes(t, m, store)
	press(m, "j", "e")
	if m.mode != modeForm {
		t.Fatal("e did not open the form")
	}
	typeText(m, " today")
	press(m, "enter") // title -> due
	for range len("2026-09-10") {
		press(m, "backspace")
	}
	press(m, "enter") // due (blank) -> issue
	typeText(m, "https://github.com/owner/repo/pull/7")
	press(m, "enter") // issue -> notes (prefilled)
	if tf, ok := m.form.(*taskForm); !ok || tf.notes != "two coats" {
		t.Fatalf("notes not prefilled: %+v", m.form)
	}
	typeText(m, ", white")
	press(m, "enter")      // notes -> status
	press(m, "j", "enter") // todo -> doing, -> project
	press(m, "j", "enter") // house -> work, submit
	if m.mode != modeBrowse || m.err != nil {
		t.Fatalf("form: mode=%v err=%v", m.mode, m.err)
	}
	tk, _ := store.GetTask(context.Background(), 1)
	if tk.Title != "paint the hall today" || tk.Due != "" || tk.Issue != "https://github.com/owner/repo/pull/7" || tk.Notes != "two coats, white" || tk.Status != task.Doing || tk.ProjectID != 2 {
		t.Errorf("edited task: %+v", tk)
	}
	// The notes come after the subtasks in the detail pane.
	detail := ansi.Strip(m.viewDetail(40))
	if i := strings.Index(detail, "\nnotes\n"); i < 0 || strings.Index(detail, "move furniture") > i {
		t.Errorf("detail pane order:\n%s", detail)
	}
	if got := labels(m); !reflect.DeepEqual(got, []string{"P:house", "T:fix the gate", "P:work", "T:paint the hall today", "S:buy paint", "S:move furniture", "T:email accountant"}) {
		t.Errorf("rows after move: %v", got)
	}
}

// TestLongDetailIsCutWithAMark checks that a task whose checklist fills
// the detail pane shows that its notes were cut, rather than dropping
// them without a word.
func TestLongDetailIsCutWithAMark(t *testing.T) {
	m, store := setup(t, nil)
	withNotes(t, m, store)
	for i := range 20 {
		if _, err := store.AddSubtask(context.Background(), 1, fmt.Sprintf("step %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	press(m, "r", "j")
	deliver(m, tea.WindowSizeMsg{Width: 100, Height: 20})
	page := plain(m)
	if strings.Contains(page, "two coats") || !strings.Contains(page, "…") {
		t.Errorf("cut notes not marked:\n%s", page)
	}
	if h := lipgloss.Height(page); h > 20 {
		t.Errorf("view is %d lines tall for a 20-line terminal", h)
	}
}

// TestChangedTracksWrites checks that the model knows whether the
// database changed while it was open, which is what the backup on quit
// goes by: every write sets it, and looking around does not.
func TestChangedTracksWrites(t *testing.T) {
	after := func(keys ...string) (changed bool, status string) {
		t.Helper()
		m, _ := setup(t, nil)
		press(m, keys...)
		if m.err != nil {
			t.Fatalf("%v: %v", keys, m.err)
		}
		return m.changed, m.status
	}
	// Each sequence ends on a status that shows it reached the path it
	// is for.
	unchanged := []struct {
		keys   []string
		status string
	}{
		{[]string{"j", "k", "G", "g", "v", "v", "f", "f", "r", "left", "right"}, "expanded house"},
		{[]string{"j", "z"}, "task #1 is still todo"}, // an open task cannot be archived: nothing written
		{[]string{"j", "d", "n"}, "kept"},             // delete declined
		{[]string{"space", "n"}, "kept"},              // finishing a project declined
		{[]string{"j", "e", "esc"}, "cancelled"},      // form cancelled
		{[]string{"j", "a", "x", "esc"}, "cancelled"}, // add cancelled with text typed
	}
	for _, c := range unchanged {
		changed, status := after(c.keys...)
		if changed || !strings.HasPrefix(status, c.status) {
			t.Errorf("%v: changed=%v status=%q, want unchanged and %q", c.keys, changed, status, c.status)
		}
	}
	changed := [][]string{
		{"j", "space"},      // task todo -> doing
		{"j", "j", "space"}, // subtask ticked
		{"j", "x"},          // task dropped
		{"j", "x", "z"},     // then archived
		{"j", "d", "y"},     // task deleted
		{"space", "y"},      // project done
		{"x"},               // project shelved
		{"j", "e", "enter", "enter", "enter", "enter", "enter", "enter"}, // edit saved, even unchanged
		{"G", "a", "x", "enter", "enter", "enter", "enter", "enter"},     // task added
		{"j", "s", "x", "enter"}, // subtask added
	}
	for _, keys := range changed {
		if ok, status := after(keys...); !ok {
			t.Errorf("%v did not mark the session changed: status=%q", keys, status)
		}
	}
}

// TestPollSeesChangesElsewhere checks that a change from another
// connection is picked up on the next poll, counts as a change for the
// backup on quit, keeps the cursor near where it was, and is left alone
// while a form or a confirmation is open.
func TestPollSeesChangesElsewhere(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasktracker.db")
	m, _ := setupAt(t, nil, path)
	ctx := context.Background()
	other, err := task.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	// Init starts the ticking, and each poll asks for the next.
	if m.Init() == nil {
		t.Error("Init did not start polling")
	}
	if _, cmd := m.Update(pollMsg{}); cmd == nil {
		t.Error("a poll did not ask for the next one")
	}
	// Nothing changed: nothing happens.
	press(m, "j")
	m.status = "as it was"
	deliver(m, pollMsg{})
	if m.status != "as it was" || m.changed {
		t.Errorf("poll with no change: status=%q changed=%v", m.status, m.changed)
	}
	// Own writes are not "elsewhere": the poll adds nothing to the status
	// and the write counts as the UI's own.
	press(m, "space")
	m.changed = false
	deliver(m, pollMsg{})
	if m.status != "task #1 doing" || m.changed {
		t.Errorf("poll after an own write: status=%q changed=%v", m.status, m.changed)
	}
	// A task added by another process shows up, with the cursor still on
	// the same task.
	if _, err := other.AddTask(ctx, task.NewTask{ProjectID: 1, Title: "clear the gutters"}); err != nil {
		t.Fatal(err)
	}
	m.changed = false
	deliver(m, pollMsg{})
	if m.status != "task #1 doing; changed elsewhere, reloaded" || !m.changed || m.err != nil {
		t.Errorf("poll after a change elsewhere: status=%q changed=%v err=%v", m.status, m.changed, m.err)
	}
	if got := labels(m); !reflect.DeepEqual(got, []string{"P:house", "T:paint the hall", "S:buy paint", "S:move furniture", "T:fix the gate", "T:clear the gutters", "P:work", "T:email accountant"}) {
		t.Errorf("rows after the poll: %v", got)
	}
	if r, _ := m.selected(); r.target() != (target{rowTask, 1}) {
		t.Errorf("cursor moved: %v", r.target())
	}
	// The selected task deleted elsewhere, subtasks and all: the cursor
	// keeps its place, which is now the next task, as it does after d.
	if err := other.DeleteTask(ctx, 1); err != nil {
		t.Fatal(err)
	}
	deliver(m, pollMsg{})
	if r, _ := m.selected(); r.target() != (target{rowTask, 2}) || len(m.rows) != 5 {
		t.Errorf("after the selected task went: cursor %v, %d rows", r.target(), len(m.rows))
	}
	// A reload for another reason, here r, also takes in, counts and
	// notes a change from elsewhere, so a poll is not the only way to
	// see one.
	if _, err := other.AddTask(ctx, task.NewTask{ProjectID: 2, Title: "chase the invoice"}); err != nil {
		t.Fatal(err)
	}
	m.changed = false
	press(m, "r")
	if !m.changed || m.status != "reloaded; changed elsewhere, reloaded" || len(m.rows) != 6 {
		t.Errorf("r after a change elsewhere: changed=%v status=%q rows=%d", m.changed, m.status, len(m.rows))
	}
	if err := other.DeleteTask(ctx, 4); err != nil { // clear the gutters
		t.Fatal(err)
	}
	m.changed = false
	deliver(m, pollMsg{})
	if !m.changed || len(m.rows) != 5 {
		t.Errorf("poll after the delete: changed=%v rows=%d", m.changed, len(m.rows))
	}
	// A save reloads too, and the change that came in with it is noted
	// after the save's own message.
	press(m, "e") // fix the gate, where the cursor is
	if _, err := other.AddTask(ctx, task.NewTask{ProjectID: 2, Title: "pay the bill"}); err != nil {
		t.Fatal(err)
	}
	press(m, "enter", "enter", "enter", "enter", "enter", "enter") // due, issue, notes, status, project, submit
	if m.mode != modeBrowse || m.status != "saved task #2; changed elsewhere, reloaded" || len(m.rows) != 6 {
		t.Errorf("save after a change elsewhere: mode=%v status=%q rows=%d", m.mode, m.status, len(m.rows))
	}
	if err := other.DeleteTask(ctx, 6); err != nil { // pay the bill
		t.Fatal(err)
	}
	deliver(m, pollMsg{})
	if len(m.rows) != 5 {
		t.Fatalf("rows after the delete: %d", len(m.rows))
	}
	// With a form open the rows stay put; the change is seen once it closes.
	press(m, "j", "e")
	if _, err := other.AddTask(ctx, task.NewTask{ProjectID: 2, Title: "file the return"}); err != nil {
		t.Fatal(err)
	}
	m.status = "editing"
	deliver(m, pollMsg{})
	if m.mode != modeForm || m.status != "editing" || len(m.rows) != 5 {
		t.Errorf("poll with a form open: mode=%v status=%q rows=%d", m.mode, m.status, len(m.rows))
	}
	press(m, "esc")
	deliver(m, pollMsg{})
	if m.mode != modeBrowse || m.status != "cancelled; changed elsewhere, reloaded" || len(m.rows) != 6 {
		t.Errorf("poll after the form closed: mode=%v status=%q rows=%d", m.mode, m.status, len(m.rows))
	}
	// With a confirmation open the rows stay put too, so y acts on the row
	// it names.
	press(m, "G", "d")
	if err := other.DeleteTask(ctx, 2); err != nil {
		t.Fatal(err)
	}
	deliver(m, pollMsg{})
	if m.mode != modeConfirmDelete || len(m.rows) != 6 {
		t.Errorf("poll with a confirmation open: mode=%v rows=%d", m.mode, len(m.rows))
	}
	press(m, "y")
	if m.err != nil || !strings.HasPrefix(m.status, "deleted task #") {
		t.Errorf("delete after a poll: status=%q err=%v", m.status, m.err)
	}
}

// TestPollHintSurvivesReload checks that a poll that reloads keeps the
// message that explains the last action, such as where a row went, and
// adds its own note after it.
func TestPollHintSurvivesReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasktracker.db")
	m, store := setupAt(t, nil, path)
	other, err := task.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	press(m, "j", "x", "z") // drop paint the hall, then archive it: hidden
	if m.status != "task #1 archived (hidden; f shows archived)" {
		t.Fatalf("status before the poll: %q", m.status)
	}
	if _, err := other.AddTask(context.Background(), task.NewTask{ProjectID: 2, Title: "file the return"}); err != nil {
		t.Fatal(err)
	}
	deliver(m, pollMsg{})
	if m.status != "task #1 archived (hidden; f shows archived); changed elsewhere, reloaded" {
		t.Errorf("status after the poll: %q", m.status)
	}
	// With nothing to say, the note stands alone.
	m.status = ""
	if _, err := other.AddTask(context.Background(), task.NewTask{ProjectID: 2, Title: "pay the bill"}); err != nil {
		t.Fatal(err)
	}
	deliver(m, pollMsg{})
	if m.status != "changed elsewhere, reloaded" {
		t.Errorf("status after a quiet poll: %q", m.status)
	}
	// Change after change does not pile the note up.
	for i := range 3 {
		if _, err := other.AddTask(context.Background(), task.NewTask{ProjectID: 2, Title: fmt.Sprintf("errand %d", i)}); err != nil {
			t.Fatal(err)
		}
		deliver(m, pollMsg{})
	}
	if m.status != "changed elsewhere, reloaded" {
		t.Errorf("status after polls in a row: %q", m.status)
	}
	// An error from the last action is left for a keypress to clear;
	// a poll's own error goes once a poll succeeds.
	m.err = errors.New("from an action")
	deliver(m, pollMsg{})
	if m.err == nil || m.err.Error() != "from an action" {
		t.Errorf("poll cleared the action's error: %v", m.err)
	}
	m.err, m.pollErr = errors.New("from a poll"), true
	deliver(m, pollMsg{})
	if m.err != nil || m.pollErr {
		t.Errorf("poll left its own error: %v", m.err)
	}
	// A keypress clears any error, and forgets where it came from, so a
	// later poll cannot clear an action's error on the strength of an
	// older poll's.
	m.err, m.pollErr = errors.New("from a poll"), true
	press(m, "k")
	if m.err != nil || m.pollErr {
		t.Errorf("keypress left the poll error: %v pollErr=%v", m.err, m.pollErr)
	}
	// A poll that cannot read the store leaves an action's error where it
	// is, and sets its own only when there is none.
	store.Close()
	m.err = errors.New("from an action")
	deliver(m, pollMsg{})
	if m.err == nil || m.err.Error() != "from an action" || m.pollErr {
		t.Errorf("failing poll over an action's error: err=%v pollErr=%v", m.err, m.pollErr)
	}
	m.err = nil
	deliver(m, pollMsg{})
	if m.err == nil || !m.pollErr {
		t.Errorf("poll on a closed store: err=%v pollErr=%v", m.err, m.pollErr)
	}
}

// TestChangedElsewhereAtQuit checks the last look Run takes on the way
// out: a change from another process that no poll has seen yet counts,
// one that a reload has taken in is already counted, and a store that
// cannot be read is taken as changed.
func TestChangedElsewhereAtQuit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasktracker.db")
	m, store := setupAt(t, nil, path)
	other, err := task.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if m.changedElsewhere() {
		t.Error("nothing written, yet changed")
	}
	press(m, "j", "space") // an own write is not from elsewhere
	if m.changedElsewhere() {
		t.Error("own write counted as from elsewhere")
	}
	if _, err := other.AddTask(context.Background(), task.NewTask{ProjectID: 2, Title: "file the return"}); err != nil {
		t.Fatal(err)
	}
	if !m.changedElsewhere() {
		t.Error("a write from elsewhere with no poll since was missed")
	}
	press(m, "r")
	if m.changedElsewhere() || !m.changed {
		t.Errorf("after a reload: elsewhere=%v changed=%v", m.changedElsewhere(), m.changed)
	}
	store.Close()
	if !m.changedElsewhere() {
		t.Error("an unreadable store was not taken as changed")
	}
}

// TestPollLandsByDeadline checks where the cursor goes by deadline when
// another process moves the selected task to another bucket, or deletes
// it. After the move, tree order and due order disagree: paint the hall
// is the first task in the tree but comes after fix the gate by
// deadline.
func TestPollLandsByDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasktracker.db")
	m, _ := setupAt(t, nil, path)
	ctx := context.Background()
	other, err := task.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	press(m, "v") // lands on paint the hall, the soonest due in house
	if r, _ := m.selected(); r.target() != (target{rowTask, 1}) {
		t.Fatalf("start: %v", r.target())
	}
	// The due date changes elsewhere: the task moves into the next 7
	// days, after fix the gate, and the cursor follows it.
	due := "2026-09-21"
	if _, err := other.UpdateTask(ctx, 1, task.TaskEdit{Due: &due}); err != nil {
		t.Fatal(err)
	}
	deliver(m, pollMsg{})
	if got := labels(m); !reflect.DeepEqual(got, []string{"H:Next 7 days", "T:fix the gate", "T:paint the hall", "S:buy paint", "S:move furniture", "H:No deadline", "T:email accountant"}) {
		t.Errorf("rows after the due date moved: %v", got)
	}
	if r, _ := m.selected(); r.target() != (target{rowTask, 1}) || m.cursor != 2 || !m.changed {
		t.Errorf("cursor after the due date moved: %v at %d, changed=%v", r.target(), m.cursor, m.changed)
	}
	// The task is deleted elsewhere. The No deadline heading slides into
	// the cursor's place, so the cursor moves on to the task under it.
	if err := other.DeleteTask(ctx, 1); err != nil {
		t.Fatal(err)
	}
	deliver(m, pollMsg{})
	if got := labels(m); !reflect.DeepEqual(got, []string{"H:Next 7 days", "T:fix the gate", "H:No deadline", "T:email accountant"}) {
		t.Errorf("rows after the delete: %v", got)
	}
	if r, _ := m.selected(); r.target() != (target{rowTask, 3}) {
		t.Errorf("cursor after the delete: %v, want email accountant", r.target())
	}
}

// TestFoldsPersist checks that folds are kept in the folds file as they
// change, and come back when a new model loads them: a fold on an area,
// a project and a by-deadline heading; a fold undone by reveal; a fold on
// a deleted project dropped; and a file that cannot be read or written
// reported without stopping anything.
func TestFoldsPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasktracker.db")
	folds := FoldsPath(path)
	if folds != path+"-folds" {
		t.Errorf("FoldsPath = %q", folds)
	}
	m, store := setupAt(t, nil, path)
	ctx := context.Background()
	garden, err := store.AddArea(ctx, task.NewArea{Name: "garden"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateProject(ctx, 1, task.ProjectEdit{AreaID: &garden.ID}); err != nil { // house
		t.Fatal(err)
	}
	m.foldsPath = folds
	if err := m.loadFolds(); err != nil {
		t.Fatalf("no file yet: %v", err)
	}
	press(m, "r")
	// Fold the area, the project work and the Overdue heading.
	press(m, "g", "left")      // garden
	press(m, "G", "left")      // email accountant: folds work
	press(m, "v", "g", "left") // Overdue heading
	if m.err != nil {
		t.Fatal(m.err)
	}
	want := map[target]bool{{rowArea, garden.ID}: true, {rowProject, 2}: true, {rowHeading, int64(bucketOverdue)}: true}
	if !reflect.DeepEqual(m.collapsed, want) {
		t.Fatalf("collapsed = %v", m.collapsed)
	}
	// Each area or project fold carries the stamp of the row's creation.
	work, err := store.GetProject(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	gardenLine := "area 1 " + stamp(garden.CreatedAt) + "\n"
	workLine := "project 2 " + stamp(work.CreatedAt) + "\n"
	data, err := os.ReadFile(folds)
	if err != nil || string(data) != gardenLine+"heading 1\n"+workLine {
		t.Errorf("folds file = %q, %v", data, err)
	}
	info, err := os.Stat(folds)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("folds file mode = %o", info.Mode().Perm())
	}

	// A fresh model with the same file opens with the same folds, and the
	// rows reflect them.
	again := newModel(ctx, store, nil)
	again.now = m.now
	again.foldsPath = folds
	if err := again.loadFolds(); err != nil {
		t.Fatal(err)
	}
	if err := again.reload(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again.collapsed, want) {
		t.Errorf("reloaded folds = %v", again.collapsed)
	}
	if got := labels(again); !reflect.DeepEqual(got, []string{"A:garden", "P:work"}) {
		t.Errorf("rows with the kept folds: %v", got)
	}

	// Revealing a saved task unfolds its project, and that is kept too.
	press(again, "G", "a")
	typeText(again, "send invoice")
	press(again, "enter", "enter", "enter", "enter", "enter")
	if again.mode != modeBrowse || again.err != nil {
		t.Fatalf("add under a folded project: mode=%v err=%v", again.mode, again.err)
	}
	if data, _ := os.ReadFile(folds); string(data) != gardenLine+"heading 1\n" {
		t.Errorf("folds file after reveal = %q", data)
	}

	// A deleted area's fold is dropped from the file by the next reload.
	if err := store.DeleteArea(ctx, garden.ID); err != nil {
		t.Fatal(err)
	}
	press(again, "r")
	if data, _ := os.ReadFile(folds); string(data) != "heading 1\n" {
		t.Errorf("folds file after the area went = %q", data)
	}

	// A kept fold on a project whose id has since gone to another
	// project is dropped: work is deleted and a new project takes id 2,
	// made at another time.
	press(again, "G", "left") // fold work again
	if err := store.DeleteProject(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if p, err := store.AddProject(ctx, task.NewProject{Name: "shed"}); err != nil || p.ID != 2 {
		t.Fatalf("new project did not take id 2: %+v, %v", p, err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE projects SET created_at = '2030-01-01T00:00:00Z' WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	fresh := newModel(ctx, store, nil)
	fresh.foldsPath = folds
	if err := fresh.loadFolds(); err != nil || !fresh.collapsed[target{rowProject, 2}] {
		t.Fatalf("fold on the old id not read: %v, folds=%v", err, fresh.collapsed)
	}
	if err := fresh.reload(); err != nil {
		t.Fatal(err)
	}
	if fresh.collapsed[target{rowProject, 2}] {
		t.Error("the new project opened folded with the old one's fold")
	}
	if data, _ := os.ReadFile(folds); strings.Contains(string(data), "project 2") {
		t.Errorf("folds file keeps the old project's fold: %q", data)
	}

	// A line that is not a fold is skipped and reported, and the rest of
	// the file is still read; so is a heading that is not one of the
	// buckets, or an area or project without its stamp. A missing file
	// is no folds and no error.
	if err := os.WriteFile(folds, []byte("project 1 2026-09-01T00:00:00Z\n\nproject one\nheading 1\nheading 9\narea 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := newModel(ctx, store, nil)
	bad.foldsPath = folds
	err = bad.loadFolds()
	if err == nil || !strings.Contains(err.Error(), "line 3") || !strings.Contains(err.Error(), "project one") || !strings.Contains(err.Error(), "2 more") {
		t.Errorf("corrupt file: %v", err)
	}
	if !reflect.DeepEqual(bad.collapsed, map[target]bool{{rowProject, 1}: true, {rowHeading, 1}: true}) {
		t.Errorf("folds read around the bad lines = %v", bad.collapsed)
	}
	// A file that cannot be read at all is reported, and nothing is saved
	// over it for the rest of the session: its folds are still in there.
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		kept, err := os.ReadFile(folds)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(folds, 0); err != nil {
			t.Fatal(err)
		}
		shut := newModel(ctx, store, nil)
		shut.foldsPath = folds
		if err := shut.loadFolds(); err == nil || !strings.Contains(err.Error(), "not saved this session") || len(shut.collapsed) != 0 {
			t.Errorf("unreadable file: %v, folds=%v", err, shut.collapsed)
		}
		if err := shut.reload(); err != nil {
			t.Fatal(err)
		}
		press(shut, "left") // folds house
		if !shut.collapsed[target{rowProject, 1}] || shut.foldsErr != nil {
			t.Errorf("fold with an unreadable file: folds=%v err=%v", shut.collapsed, shut.foldsErr)
		}
		if err := os.Chmod(folds, 0o600); err != nil {
			t.Fatal(err)
		}
		if now, _ := os.ReadFile(folds); string(now) != string(kept) {
			t.Errorf("unreadable file was written over: %q", now)
		}
	}
	if err := os.Remove(folds); err != nil {
		t.Fatal(err)
	}
	if err := bad.loadFolds(); err != nil || len(bad.collapsed) != 0 {
		t.Errorf("missing file: %v, folds=%v", err, bad.collapsed)
	}

	// A save that fails is shown, and the fold still happens; a key
	// clears the message, and a save that works clears the trouble.
	bad.foldsPath = filepath.Join(t.TempDir(), "gone", "folds")
	if err := bad.reload(); err != nil {
		t.Fatal(err)
	}
	press(bad, "left")
	if bad.foldsErr == nil || !strings.Contains(bad.foldsErr.Error(), "folds") || len(bad.collapsed) != 1 || !strings.Contains(plain(bad), "error: folds") {
		t.Errorf("failed save: foldsErr=%v folds=%v\n%s", bad.foldsErr, bad.collapsed, plain(bad))
	}
	press(bad, "j")
	if bad.foldsErr != nil || strings.Contains(plain(bad), "error:") {
		t.Errorf("key did not clear the folds error: %v", bad.foldsErr)
	}
	// The save that follows a prune fails the same way, and is shown even
	// though reload itself went well: a deleted area's fold is pruned by
	// the reload after r.
	bad.collapsed[target{rowArea, 99}] = true
	bad.foldMade[target{rowArea, 99}] = stamp(fixed)
	press(bad, "r")
	if bad.err != nil || bad.foldsErr == nil || bad.collapsed[target{rowArea, 99}] {
		t.Errorf("failed save after a prune: err=%v foldsErr=%v folds=%v", bad.err, bad.foldsErr, bad.collapsed)
	}
	bad.foldsPath = folds
	press(bad, "g", "left") // house, folded above: unfolds, which saves
	if bad.foldsErr != nil || bad.collapsed[target{rowProject, 1}] {
		t.Errorf("a save that works: err=%v folds=%v", bad.foldsErr, bad.collapsed)
	}
	// A save whose rename fails, here onto a directory in the way, is
	// reported and leaves no temporary file behind.
	inWay := filepath.Join(t.TempDir(), "folds")
	if err := os.MkdirAll(filepath.Join(inWay, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	bad.foldsPath = inWay
	press(bad, "left") // folds house again, which saves
	if bad.foldsErr == nil || !bad.collapsed[target{rowProject, 1}] {
		t.Errorf("rename onto a directory: err=%v folds=%v", bad.foldsErr, bad.collapsed)
	}
	entries, err := os.ReadDir(filepath.Dir(inWay))
	if err != nil || len(entries) != 1 {
		t.Errorf("after a failed rename, the directory holds %v, %v", entries, err)
	}
	// A save that fails inside the reload after an action keeps the
	// action's message, with the folds error after it: here house, still
	// folded, is deleted, and its fold is pruned.
	press(bad, "d", "y")
	if bad.err != nil || bad.collapsed[target{rowProject, 1}] {
		t.Fatalf("delete of a folded project: err=%v folds=%v", bad.err, bad.collapsed)
	}
	if line := bad.viewStatus(); !strings.HasPrefix(ansi.Strip(line), "deleted project #1; error: folds: ") {
		t.Errorf("status after the delete = %q", ansi.Strip(line))
	}
}

// TestUnrecognisedIssue covers a stored issue that ParseIssue did not
// write, as from a hand-edited database: the detail pane shows it without
// its control characters, and the edit form keeps it and saves the other
// fields.
func TestUnrecognisedIssue(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tasktracker.db")
	store, err := task.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	p, err := store.AddProject(ctx, task.NewProject{Name: "garden"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddTask(ctx, task.NewTask{ProjectID: p.ID, Title: "mend the fence"}); err != nil {
		t.Fatal(err)
	}
	const odd = "the fence ticket\x1b[2J"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE tasks SET issue = ? WHERE id = 1`, odd); err != nil {
		t.Fatal(err)
	}
	db.Close()
	m := newModel(ctx, store, nil)
	m.now = func() time.Time { return fixed }
	if err := m.reload(); err != nil {
		t.Fatal(err)
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	press(m, "j")
	if !strings.Contains(plain(m), "issue   the fence ticket[2J") {
		t.Errorf("odd issue not in the detail pane:\n%s", plain(m))
	}
	if strings.Contains(m.View().Content, "\x1b[2J") {
		t.Error("the stored issue's escape sequence reached the screen")
	}

	press(m, "e")
	typeText(m, " today")
	press(m, "enter", "enter", "enter", "enter", "enter", "enter") // title, due, issue, notes, status, project
	if m.mode != modeBrowse || m.err != nil {
		t.Fatalf("form: mode=%v err=%v", m.mode, m.err)
	}
	if got, _ := store.GetTask(ctx, 1); got.Title != "mend the fence today" || got.Issue != odd {
		t.Errorf("after editing the title: %+v", got)
	}
}

func TestSubtaskForms(t *testing.T) {
	m, store := setup(t, nil)
	ctx := context.Background()
	press(m, "s") // on a project: refused
	if m.mode != modeBrowse || !strings.Contains(m.status, "select a task") {
		t.Errorf("s on project: mode=%v status=%q", m.mode, m.status)
	}
	press(m, "j", "j", "s") // on a subtask: adds a sibling under the same task
	if help := m.helpLine(); strings.Contains(help, "ctrl+j") || !strings.Contains(help, "enter save") {
		t.Errorf("subtask form help = %q", help)
	}
	typeText(m, "wash brushes")
	press(m, "enter")
	if m.mode != modeBrowse || m.err != nil {
		t.Fatalf("subtask form: mode=%v err=%v", m.mode, m.err)
	}
	subs, _ := store.ListSubtasks(ctx, 1)
	if len(subs) != 3 || subs[2].Title != "wash brushes" {
		t.Errorf("subtasks: %+v", subs)
	}
	if r, _ := m.selected(); r.subtask.Title != "wash brushes" {
		t.Errorf("cursor: %v", r.target())
	}
	press(m, "e")
	typeText(m, " well")
	press(m, "enter")
	if s, _ := store.GetSubtask(ctx, subs[2].ID); s.Title != "wash brushes well" {
		t.Errorf("renamed subtask: %+v", s)
	}
}

func TestProjectFormWithGoalPicks(t *testing.T) {
	m, store := setup(t, goaltrackerDB(t))
	ctx := context.Background()
	press(m, "A")
	if m.mode != modeForm {
		t.Fatal("A did not open the form")
	}
	pf, ok := m.form.(*projectForm)
	if !ok || !pf.pickList {
		t.Fatalf("form = %T, pickList=%v", m.form, ok && pf.pickList)
	}
	typeText(m, "garden")
	press(m, "enter") // name -> about
	typeText(m, "the back")
	press(m, "ctrl+j")
	typeText(m, "and the front")
	press(m, "enter")           // about -> goals
	press(m, "j", "x", "enter") // pick the second goal (#5), submit
	if m.mode != modeBrowse || m.err != nil {
		t.Fatalf("project form: mode=%v err=%v", m.mode, m.err)
	}
	projects, _ := store.ListProjects(ctx, task.ProjectFilter{})
	p := projects[len(projects)-1]
	if p.Name != "garden" || p.Description != "the back\nand the front" || !reflect.DeepEqual(p.GoalIDs, []int64{3}) {
		t.Errorf("saved project: %+v", p)
	}
	if r, _ := m.selected(); r.target() != (target{rowProject, p.ID}) {
		t.Errorf("cursor: %v", r.target())
	}
	if !strings.Contains(plain(m), "#3 finish the garden") {
		t.Error("new project's goal label not shown")
	}

	// Editing shows the state field; shelve it and unpick the goal.
	press(m, "e")
	press(m, "enter", "enter")  // name, about
	press(m, "j", "j", "enter") // state: active -> shelved
	press(m, "j", "x", "enter") // goals: unpick #3
	if m.mode != modeBrowse || m.err != nil {
		t.Fatalf("edit form: mode=%v err=%v", m.mode, m.err)
	}
	p, _ = store.GetProject(ctx, p.ID)
	if p.State != task.Shelved || p.GoalIDs != nil {
		t.Errorf("edited project: %+v", p)
	}
}

func TestProjectFormWithTypedGoals(t *testing.T) {
	m, store := setup(t, nil)
	press(m, "e") // house
	pf, ok := m.form.(*projectForm)
	if !ok || pf.pickList || pf.goalText != "3" {
		t.Fatalf("form = %T, pickList=%v, goalText=%q", m.form, ok && pf.pickList, pf.goalText)
	}
	press(m, "enter", "enter", "enter") // name, about, state
	typeText(m, ", 9, bad")
	press(m, "enter") // rejected by validation
	if m.mode != modeForm {
		t.Fatal("bad goal id accepted")
	}
	for range 5 {
		press(m, "backspace")
	}
	press(m, "enter")
	if m.mode != modeBrowse || m.err != nil {
		t.Fatalf("form: mode=%v err=%v", m.mode, m.err)
	}
	p, _ := store.GetProject(context.Background(), 1)
	if !reflect.DeepEqual(p.GoalIDs, []int64{3, 9}) {
		t.Errorf("goal ids: %v", p.GoalIDs)
	}
}

func TestFit(t *testing.T) {
	cases := []struct {
		left, right string
		w           int
	}{
		{"short", "2026-09-10", 30},
		{"a long title that will not fit in the pane at all", "1/2  2026-09-10", 20},
		{"日本語のタスクをここに書く", "3 open", 14},
		{"no right side", "", 8},
	}
	for _, c := range cases {
		got := fit(c.left, c.right, c.w)
		if lipgloss.Width(got) != c.w {
			t.Errorf("fit(%q,%q,%d) width = %d want %d: %q", c.left, c.right, c.w, lipgloss.Width(got), c.w, got)
		}
		if !strings.HasSuffix(got, c.right) {
			t.Errorf("fit(%q,%q,%d) = %q, right side not flush", c.left, c.right, c.w, got)
		}
	}
}

func TestNarrowTerminalDoesNotOverflow(t *testing.T) {
	m, _ := setup(t, nil)
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	view := plain(m)
	if h := lipgloss.Height(view); h > 12 {
		t.Errorf("view is %d lines tall for a 12-line terminal:\n%s", h, view)
	}
	for _, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > 40 {
			t.Errorf("line %d wide: %q", w, line)
		}
	}
}

func TestDueWords(t *testing.T) {
	for days, want := range map[int]string{-3: "3 days overdue", -1: "1 day overdue", 0: "today", 1: "tomorrow", 4: "in 4 days"} {
		if got := dueWords(days); got != want {
			t.Errorf("dueWords(%d) = %q want %q", days, got, want)
		}
	}
}

// withAreas puts the setup data into areas: home holds garden, which holds
// house; work sits in home itself. Returns the ids of home and garden.
func withAreas(t *testing.T, m *model, store *task.Store) (home, garden int64) {
	t.Helper()
	ctx := context.Background()
	a, err := store.AddArea(ctx, task.NewArea{Name: "home"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.AddArea(ctx, task.NewArea{Name: "garden", ParentID: a.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateProject(ctx, 1, task.ProjectEdit{AreaID: &s.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateProject(ctx, 2, task.ProjectEdit{AreaID: &a.ID}); err != nil {
		t.Fatal(err)
	}
	if err := m.reload(); err != nil {
		t.Fatal(err)
	}
	m.cursor = 0 // reload kept the cursor on house; start from the top
	return a.ID, s.ID
}

func TestAreaRows(t *testing.T) {
	m, store := setup(t, nil)
	withAreas(t, m, store)
	want := []string{"A:home", "A:garden", "P:house", "T:paint the hall", "S:buy paint", "S:move furniture", "T:fix the gate", "P:work", "T:email accountant"}
	if got := labels(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v\nwant   %v", got, want)
	}
	view := plain(m)
	for _, want := range []string{"▾ home", "  ▾ garden", "    ▾ house", "      ○ paint the hall", "        [x] buy paint", "  ▾ work", "    ○ email accountant", "3 open", "holds   1 areas, 2 projects", "tasks   3 open"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
	press(m, "j", "j") // house
	if view := plain(m); !strings.Contains(view, "area  home / garden") {
		t.Errorf("project detail missing its area:\n%s", view)
	}
	press(m, "k", "space")
	if !strings.Contains(m.status, "no state") {
		t.Errorf("space on an area: %q", m.status)
	}
	press(m, "x")
	if !strings.Contains(m.status, "no state") {
		t.Errorf("x on an area: %q", m.status)
	}
	press(m, "a")
	if m.mode != modeBrowse || !strings.Contains(m.status, "select a project") {
		t.Errorf("a on an area: mode=%v status=%q", m.mode, m.status)
	}
	press(m, "s")
	if m.mode != modeBrowse || !strings.Contains(m.status, "select a task") {
		t.Errorf("s on an area: mode=%v status=%q", m.mode, m.status)
	}
}

func TestCollapse(t *testing.T) {
	m, store := setup(t, nil)
	ctx := context.Background()
	press(m, "left") // fold house
	want := []string{"P:house", "P:work", "T:email accountant"}
	if got := labels(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows after left = %v\nwant %v", got, want)
	}
	if m.cursor != 0 || !strings.Contains(m.status, "collapsed house") {
		t.Errorf("after left: cursor=%d status=%q", m.cursor, m.status)
	}
	view := plain(m)
	for _, want := range []string{"▸ house", "2 open", "▾ work", "collapsed; ← shows its tasks"} {
		if !strings.Contains(view, want) {
			t.Errorf("collapsed view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "paint the hall") {
		t.Errorf("collapsed view still lists tasks:\n%s", view)
	}
	press(m, "left", "left") // the same key unfolds, and folds again
	if got := labels(m); !reflect.DeepEqual(got, want) {
		t.Errorf("rows after left twice more = %v", got)
	}
	press(m, "h", "l") // not bound any more
	if got := labels(m); !reflect.DeepEqual(got, want) || m.cursor != 0 {
		t.Errorf("after h and l: rows=%v cursor=%d", got, m.cursor)
	}

	// The fold survives a reload and an edit elsewhere.
	press(m, "G", "space") // email accountant -> doing
	if got := labels(m); !reflect.DeepEqual(got, want) {
		t.Errorf("rows after an edit = %v", got)
	}
	press(m, "right") // on a task: folds work and lands on it
	if got := labels(m); !reflect.DeepEqual(got, []string{"P:house", "P:work"}) {
		t.Errorf("rows after right on a task = %v", got)
	}
	if r, _ := m.selected(); r.target() != (target{rowProject, 2}) {
		t.Errorf("cursor after right on a task: %v", r.target())
	}

	// Adding a task to a collapsed project shows it again.
	press(m, "a")
	typeText(m, "send invoice")
	press(m, "enter", "enter", "enter", "enter", "enter") // title -> due -> issue -> notes -> project -> submit
	if m.mode != modeBrowse || m.err != nil {
		t.Fatalf("task form: mode=%v err=%v", m.mode, m.err)
	}
	if r, _ := m.selected(); r.kind != rowTask || r.task.Task.Title != "send invoice" {
		t.Errorf("cursor after adding to a collapsed project: %v", r.target())
	}
	if got := labels(m); !reflect.DeepEqual(got, []string{"P:house", "P:work", "T:email accountant", "T:send invoice"}) {
		t.Errorf("rows after adding = %v", got)
	}

	press(m, "g", "right") // the right arrow unfolds too
	if got := labels(m); got[1] != "T:paint the hall" || len(got) != 8 {
		t.Errorf("rows after right = %v", got)
	}
	if m.cursor != 0 || m.status != "expanded house" {
		t.Errorf("after right: cursor=%d status=%q", m.cursor, m.status)
	}
	press(m, "j", "left", "right") // on a task: fold the project, then unfold it
	if got := labels(m); len(got) != 8 || m.cursor != 0 {
		t.Errorf("after left then right from a task: rows=%v cursor=%d", got, m.cursor)
	}

	// A project with nothing listed has nothing to fold.
	p, err := store.AddProject(ctx, task.NewProject{Name: "empty"})
	if err != nil {
		t.Fatal(err)
	}
	press(m, "r", "G")
	if r, _ := m.selected(); r.target() != (target{rowProject, p.ID}) {
		t.Fatalf("cursor: %v", r.target())
	}
	press(m, "left")
	if !strings.Contains(m.status, "nothing to hide") || m.collapsed[target{rowProject, p.ID}] {
		t.Errorf("left on an empty project: %q", m.status)
	}

	// By deadline, the arrows fold headings.
	press(m, "v", "g", "left", "left") // fold and unfold a heading
	if !strings.HasPrefix(m.status, "expanded ") {
		t.Errorf("left twice in the deadline view: %q", m.status)
	}
	press(m, "v")

	// A fold whose contents have since gone can still be undone: fold work
	// while its archived task shows, hide archived, then unfold.
	if err := store.MarkTask(ctx, 3, task.Finished); err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveTask(ctx, 3, true); err != nil {
		t.Fatal(err)
	}
	press(m, "f") // showing archived; work still lists email accountant
	m.selectTarget(target{rowProject, 2})
	press(m, "left")
	if !m.collapsed[target{rowProject, 2}] {
		t.Fatal("work did not fold")
	}
	press(m, "f") // hiding archived; work now lists nothing
	m.selectTarget(target{rowProject, 2})
	press(m, "left")
	if m.collapsed[target{rowProject, 2}] || m.status != "expanded work" {
		t.Errorf("unfolding an emptied project: %q", m.status)
	}
	if !strings.Contains(plain(m), "▾ work") {
		t.Errorf("view after unfolding:\n%s", plain(m))
	}

	// A deleted project's fold is forgotten, so a project that reuses its
	// id does not start folded.
	m.selectTarget(target{rowProject, p.ID})
	if _, err := store.AddTask(ctx, task.NewTask{ProjectID: p.ID, Title: "one"}); err != nil {
		t.Fatal(err)
	}
	press(m, "r", "left")
	if !m.collapsed[target{rowProject, p.ID}] {
		t.Fatal("empty did not fold")
	}
	press(m, "d", "y")
	if m.err != nil {
		t.Fatal(m.err)
	}
	again, err := store.AddProject(ctx, task.NewProject{Name: "again"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddTask(ctx, task.NewTask{ProjectID: again.ID, Title: "two"}); err != nil {
		t.Fatal(err)
	}
	press(m, "r")
	if again.ID != p.ID {
		t.Logf("id not reused (%d vs %d); the prune is checked directly", again.ID, p.ID)
	}
	if m.collapsed[target{rowProject, p.ID}] {
		t.Error("a deleted project's fold survived reload")
	}
	if got := labels(m); got[len(got)-1] != "T:two" {
		t.Errorf("rows after re-adding = %v", got)
	}

	// A finished project hidden by f is not deleted, so its fold is kept
	// for when it shows again.
	if err := store.MarkProject(ctx, again.ID, task.Done); err != nil {
		t.Fatal(err)
	}
	press(m, "f") // showing finished
	m.selectTarget(target{rowProject, again.ID})
	press(m, "left")
	if !m.collapsed[target{rowProject, again.ID}] {
		t.Fatal("again did not fold")
	}
	press(m, "f", "f") // hidden, then shown again
	if !m.collapsed[target{rowProject, again.ID}] {
		t.Error("a hidden project's fold was forgotten")
	}
	m.selectTarget(target{rowProject, again.ID})
	if !strings.Contains(plain(m), "▸ again") {
		t.Errorf("view after showing finished again:\n%s", plain(m))
	}
}

func TestFoldArea(t *testing.T) {
	m, store := setup(t, nil)
	ctx := context.Background()
	home, garden := withAreas(t, m, store)
	press(m, "j", "left") // fold garden: house and its tasks go
	want := []string{"A:home", "A:garden", "P:work", "T:email accountant"}
	if got := labels(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows after folding garden = %v\nwant %v", got, want)
	}
	if !strings.Contains(m.status, "collapsed garden") {
		t.Errorf("status: %q", m.status)
	}
	view := plain(m)
	for _, want := range []string{"▾ home", "  ▸ garden", "collapsed; ← shows what is in it", "tasks   2 open"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
	press(m, "k", "right") // fold home: everything in it goes, garden's fold kept
	if got := labels(m); !reflect.DeepEqual(got, []string{"A:home"}) {
		t.Errorf("rows after folding home = %v", got)
	}
	press(m, "left")
	if got := labels(m); !reflect.DeepEqual(got, want) {
		t.Errorf("rows after unfolding home = %v", got)
	}

	// Adding a project inside a folded area shows the area's contents.
	press(m, "j", "A") // on garden
	typeText(m, "grant")
	press(m, "enter", "enter", "enter", "enter") // name, about, area (garden kept), goals -> submit
	if m.mode != modeBrowse || m.err != nil {
		t.Fatalf("project form: mode=%v err=%v", m.mode, m.err)
	}
	if r, _ := m.selected(); r.kind != rowProject || r.project.Project.Name != "grant" {
		t.Errorf("cursor after adding into a folded area: %v", r.target())
	}
	if m.collapsed[target{rowArea, garden}] {
		t.Error("garden still collapsed after adding a project into it")
	}
	if got := labels(m); !strings.HasPrefix(strings.Join(got, " "), "A:home A:garden P:house T:paint the hall") || got[len(got)-3] != "P:grant" {
		t.Errorf("rows after adding = %v", got)
	}

	// An empty area has nothing to fold.
	e, err := store.AddArea(ctx, task.NewArea{Name: "empty"})
	if err != nil {
		t.Fatal(err)
	}
	press(m, "r")
	m.selectTarget(target{rowArea, e.ID})
	press(m, "left")
	if !strings.Contains(m.status, "area is empty") || m.collapsed[target{rowArea, e.ID}] {
		t.Errorf("left on an empty area: %q", m.status)
	}

	// A subtask saved inside a folded project inside a folded area unfolds
	// both.
	m.collapsed[target{rowArea, home}] = true
	m.collapsed[target{rowProject, 1}] = true
	m.rebuildRows()
	m.reveal(target{rowSubtask, 1})
	if r, _ := m.selected(); r.target() != (target{rowSubtask, 1}) {
		t.Errorf("reveal landed on %v", r.target())
	}
	if len(m.collapsed) != 0 {
		t.Errorf("still collapsed: %v", m.collapsed)
	}
}

func TestAreaForms(t *testing.T) {
	m, store := setup(t, nil)
	ctx := context.Background()
	home, garden := withAreas(t, m, store)
	press(m, "j", "n") // on garden: a new area inside it
	af, ok := m.form.(*areaForm)
	if !ok || af.parent != garden {
		t.Fatalf("form = %T parent=%d", m.form, af.parent)
	}
	typeText(m, "grant")
	press(m, "enter", "enter") // name -> inside (garden kept) -> submit
	if m.mode != modeBrowse || m.err != nil {
		t.Fatalf("area form: mode=%v err=%v", m.mode, m.err)
	}
	areas, _ := store.ListAreas(ctx)
	grant := areas[len(areas)-1]
	if grant.Name != "grant" || grant.ParentID != garden {
		t.Errorf("saved area: %+v", grant)
	}
	if r, _ := m.selected(); r.target() != (target{rowArea, grant.ID}) {
		t.Errorf("cursor: %v", r.target())
	}
	if got := labels(m); got[2] != "A:grant" {
		t.Errorf("rows: %v", got)
	}

	// Editing garden cannot offer garden or grant as a home; move it to the top.
	press(m, "k", "e")
	af, ok = m.form.(*areaForm)
	if !ok || af.editID != garden || af.name != "garden" {
		t.Fatalf("edit form = %T %+v", m.form, af)
	}
	typeText(m, " work")
	press(m, "enter") // name -> inside
	if _, ok := af.form.GetFocusedField().(*huh.Select[int64]); !ok {
		t.Fatalf("second field = %T", af.form.GetFocusedField())
	}
	// The list is (top level) and home only: garden and grant are left out.
	// The select starts on home and wraps, so j reaches the top; were
	// garden offered, j would land on it instead.
	press(m, "j", "enter")
	if m.mode != modeBrowse || m.err != nil {
		t.Fatalf("edit form: mode=%v err=%v", m.mode, m.err)
	}
	a, _ := store.GetArea(ctx, garden)
	if a.Name != "garden work" || a.ParentID != 0 {
		t.Errorf("edited area: %+v", a)
	}
	if got := labels(m); got[0] != "A:home" || got[1] != "P:work" || got[3] != "A:garden work" {
		t.Errorf("rows after the move: %v", got)
	}

	// A new project from an area row lands in that area.
	press(m, "g", "A")
	pf, ok := m.form.(*projectForm)
	if !ok || pf.area != home {
		t.Fatalf("project form = %T area=%d", m.form, pf.area)
	}
	typeText(m, "docs")
	press(m, "enter", "enter", "enter", "enter") // name, about, area, goals -> submit
	if m.mode != modeBrowse || m.err != nil {
		t.Fatalf("project form: mode=%v err=%v", m.mode, m.err)
	}
	projects, _ := store.ListProjects(ctx, task.ProjectFilter{})
	p := projects[len(projects)-1]
	if p.Name != "docs" || p.AreaID != home {
		t.Errorf("saved project: %+v", p)
	}
	// Editing it and picking (none) moves it out.
	press(m, "e")
	press(m, "enter", "enter") // name, about
	press(m, "g", "enter")     // area: (none)
	press(m, "enter", "enter") // state, goals
	if m.mode != modeBrowse || m.err != nil {
		t.Fatalf("project edit: mode=%v err=%v", m.mode, m.err)
	}
	if p, _ = store.GetProject(ctx, p.ID); p.AreaID != 0 {
		t.Errorf("project after picking (none): %+v", p)
	}
}

func TestDeleteArea(t *testing.T) {
	m, store := setup(t, nil)
	ctx := context.Background()
	home, garden := withAreas(t, m, store)
	press(m, "j", "d")
	if m.mode != modeConfirmDelete || !strings.Contains(plain(m), `delete area #2 "garden" and move what is in it up a level?`) {
		t.Fatalf("d on an area: mode=%v\n%s", m.mode, plain(m))
	}
	press(m, "y")
	if m.err != nil || m.status != "deleted area #2" {
		t.Errorf("after delete: err=%v status=%q", m.err, m.status)
	}
	if _, err := store.GetArea(ctx, garden); err == nil {
		t.Error("area still there")
	}
	if p, _ := store.GetProject(ctx, 1); p.AreaID != home {
		t.Errorf("house after the delete: area %d", p.AreaID)
	}
	if got := labels(m); !reflect.DeepEqual(got[:2], []string{"A:home", "P:house"}) {
		t.Errorf("rows after the delete: %v", got)
	}
}
