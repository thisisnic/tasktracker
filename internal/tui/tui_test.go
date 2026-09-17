package tui

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
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
	store, err := task.Open(filepath.Join(t.TempDir(), "tasktracker.db"))
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

// labels lists the rows as "kind:title" for comparing tree shapes.
func labels(m *model) []string {
	var out []string
	for _, r := range m.rows {
		switch r.kind {
		case rowProject:
			out = append(out, "P:"+r.project.Project.Name)
		case rowTask:
			out = append(out, "T:"+r.task.Task.Title)
		case rowSubtask:
			out = append(out, "S:"+r.subtask.Title)
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
	for _, want := range []string{"tasktracker · tree", "house", "2 open", "fix it up", "#3", "paint the hall", "1/2", "2026-09-10", "[x] buy paint", "[ ] move furniture"} {
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
	// Done tasks are hidden; the status line says so and the cursor stays put.
	if got := labels(m); got[1] != "T:fix the gate" || !strings.Contains(m.status, "hidden") {
		t.Errorf("after done: rows=%v status=%q", got, m.status)
	}
	press(m, "f")
	if got := labels(m); got[1] != "T:paint the hall" || !m.showAll {
		t.Errorf("f did not show finished: %v", got)
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
	press(m, "g", "space") // project: active -> done
	if p, _ := store.GetProject(ctx, 1); p.State != task.Done {
		t.Errorf("space on project: %s", p.State)
	}
	press(m, "space", "space")
	if p, _ := store.GetProject(ctx, 1); p.State != task.Active {
		t.Errorf("project did not cycle back to active: %s", p.State)
	}
}

func TestDropAndShelve(t *testing.T) {
	m, store := setup(t, nil)
	ctx := context.Background()
	press(m, "j", "x")
	if tk, _ := store.GetTask(ctx, 1); tk.Status != task.Dropped {
		t.Errorf("x on task: %s", tk.Status)
	}
	if got := labels(m); got[1] != "T:fix the gate" {
		t.Errorf("dropped task still listed: %v", got)
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

func TestDueView(t *testing.T) {
	m, _ := setup(t, nil)
	press(m, "v")
	if m.view != viewDue {
		t.Fatal("v did not switch to the due view")
	}
	if got := labels(m); !reflect.DeepEqual(got, []string{"T:paint the hall", "T:fix the gate"}) {
		t.Errorf("due rows = %v", got)
	}
	view := plain(m)
	if !strings.Contains(view, "tasktracker · due") || !strings.Contains(view, "house") {
		t.Errorf("due view:\n%s", view)
	}
	press(m, "space", "space") // paint the hall -> done, leaves the list
	if got := labels(m); !reflect.DeepEqual(got, []string{"T:fix the gate"}) {
		t.Errorf("after finishing: %v", got)
	}
	press(m, "space", "space")
	if len(m.rows) != 0 || !strings.Contains(plain(m), "nothing due") {
		t.Errorf("empty due view: rows=%d", len(m.rows))
	}
	press(m, "v")
	if m.view != viewTree || len(m.rows) == 0 {
		t.Error("v did not go back to the tree")
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
	press(m, "enter") // due -> project (prefilled: work)
	press(m, "enter") // submit
	if m.mode != modeBrowse || m.err != nil {
		t.Fatalf("form did not close cleanly: mode=%v err=%v", m.mode, m.err)
	}
	tasks, _ := store.ListTasks(context.Background(), task.TaskFilter{ProjectID: 2})
	if len(tasks) != 2 || tasks[1].Title != "send invoice" || tasks[1].Due != "2026-09-18" {
		t.Errorf("saved task: %+v", tasks)
	}
	if r, _ := m.selected(); r.target() != (target{rowTask, tasks[1].ID}) {
		t.Errorf("cursor not on the new task: %v", r.target())
	}
	if !strings.Contains(plain(m), "tomorrow") {
		t.Error("due words not shown")
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

func TestEditTaskViaForm(t *testing.T) {
	m, store := setup(t, nil)
	press(m, "j", "e")
	if m.mode != modeForm {
		t.Fatal("e did not open the form")
	}
	typeText(m, " today")
	press(m, "enter") // title -> due
	for range len("2026-09-10") {
		press(m, "backspace")
	}
	press(m, "enter")      // due (blank) -> status
	press(m, "j", "enter") // todo -> doing, -> project
	press(m, "j", "enter") // house -> work, submit
	if m.mode != modeBrowse || m.err != nil {
		t.Fatalf("form: mode=%v err=%v", m.mode, m.err)
	}
	tk, _ := store.GetTask(context.Background(), 1)
	if tk.Title != "paint the hall today" || tk.Due != "" || tk.Status != task.Doing || tk.ProjectID != 2 {
		t.Errorf("edited task: %+v", tk)
	}
	if got := labels(m); !reflect.DeepEqual(got, []string{"P:house", "T:fix the gate", "P:work", "T:paint the hall today", "S:buy paint", "S:move furniture", "T:email accountant"}) {
		t.Errorf("rows after move: %v", got)
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
