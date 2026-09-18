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

func TestFormStaysOpenWhenSaveFails(t *testing.T) {
	m, store := setup(t, nil)
	press(m, "j", "e")
	// Pull the task out from under the form so the save fails.
	if err := store.DeleteTask(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	typeText(m, " again")
	press(m, "enter", "enter", "enter", "enter")
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

func TestDueViewRowStyling(t *testing.T) {
	m, _ := setup(t, nil)
	press(m, "v", "j") // fix the gate selected; paint the hall is overdue and unselected
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
		t.Fatalf("rows after h = %v\nwant %v", got, want)
	}
	if m.cursor != 0 || !strings.Contains(m.status, "collapsed house") {
		t.Errorf("after h: cursor=%d status=%q", m.cursor, m.status)
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
		t.Errorf("rows after h on a task = %v", got)
	}
	if r, _ := m.selected(); r.target() != (target{rowProject, 2}) {
		t.Errorf("cursor after h on a task: %v", r.target())
	}

	// Adding a task to a collapsed project shows it again.
	press(m, "a")
	typeText(m, "send invoice")
	press(m, "enter", "enter", "enter") // title -> due -> project -> submit
	if m.mode != modeBrowse || m.err != nil {
		t.Fatalf("task form: mode=%v err=%v", m.mode, m.err)
	}
	if r, _ := m.selected(); r.kind != rowTask || r.task.Task.Title != "send invoice" {
		t.Errorf("cursor after adding to a collapsed project: %v", r.target())
	}
	if got := labels(m); !reflect.DeepEqual(got, []string{"P:house", "P:work", "T:email accountant", "T:send invoice"}) {
		t.Errorf("rows after adding = %v", got)
	}

	press(m, "g", "right") // the right home unfolds too
	if got := labels(m); got[1] != "T:paint the hall" || len(got) != 8 {
		t.Errorf("rows after l = %v", got)
	}
	if m.cursor != 0 || m.status != "expanded house" {
		t.Errorf("after l: cursor=%d status=%q", m.cursor, m.status)
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

	// The due list is flat.
	press(m, "v", "left")
	if !strings.Contains(m.status, "flat") {
		t.Errorf("h in the due view: %q", m.status)
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
