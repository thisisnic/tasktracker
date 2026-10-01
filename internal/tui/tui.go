// Package tui is the terminal UI for tasktracker, built on Bubble Tea v2.
//
// The left pane lists tasks one of two ways: by project, as a tree of
// areas, projects, tasks and subtasks, or by deadline, as one list of
// tasks soonest due first. A project in the tree can be collapsed to hide
// its tasks, and an area to hide everything in it. The right pane shows
// the selected item. Forms for adding and editing take over both panes.
package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/thisisnic/tasktracker/internal/goallink"
	"github.com/thisisnic/tasktracker/internal/task"
)

// Options adjust how the TUI starts.
type Options struct {
	// Goals looks up the statements of goals a project links to. Nil means
	// goal ids are shown bare.
	Goals *goallink.Reader
	// ShowFinished starts with archived tasks and finished projects
	// visible.
	ShowFinished bool
}

// Run opens the by-project view and blocks until the user quits. It
// reports whether the UI wrote to the database, which is what decides
// whether a backup on quit is worth taking.
func Run(ctx context.Context, store *task.Store, opts Options) (changed bool, err error) {
	m := newModel(ctx, store, opts.Goals)
	m.showAll = opts.ShowFinished
	if err := m.reload(); err != nil {
		return false, err
	}
	_, err = tea.NewProgram(m, tea.WithContext(ctx)).Run()
	return m.changed, err
}

type mode int

const (
	modeBrowse mode = iota
	modeConfirmDelete
	modeConfirmState // space on a project: leaving active hides it, so ask first
	modeForm
)

// viewKind is how the left pane is arranged: by project, as a tree, or by
// deadline, as one list of tasks.
type viewKind int

const (
	viewTree viewKind = iota
	viewDeadline
)

func (v viewKind) String() string {
	if v == viewDeadline {
		return "by deadline"
	}
	return "by project"
}

// rowKind says what a row in the left pane stands for.
type rowKind int

const (
	rowArea rowKind = iota
	rowProject
	rowTask
	rowSubtask
	rowHeading // a group of tasks by deadline: how soon they are due
)

// row is one selectable line in the left pane. Area is set for area rows.
// Project is set for every project, task and subtask row: the project the
// row belongs to. Task is set for task and subtask rows. Depth is how many
// areas the row is inside, and sets its indent. A heading row has the
// bucket it stands for and how many tasks are in it.
type row struct {
	kind    rowKind
	depth   int
	area    task.AreaNode
	project task.ProjectNode
	task    task.TaskNode
	subtask task.Subtask
	bucket  bucket
	count   int
}

// bucket is how soon a task is due, for the by-deadline headings. The
// values are the headings' ids in the fold map, so they never change.
type bucket int64

const (
	bucketOverdue bucket = iota + 1
	bucketWeek
	bucketMonth
	bucketLonger
	bucketNone
)

func (b bucket) String() string {
	switch b {
	case bucketOverdue:
		return "Overdue"
	case bucketWeek:
		return "Next 7 days"
	case bucketMonth:
		return "Next 30 days"
	case bucketLonger:
		return "Longer"
	}
	return "No deadline"
}

// bucketOf says how soon a due date is. The next 7 days start today; the
// next 30 start where the 7 end.
func bucketOf(due string, today time.Time) bucket {
	days, ok := task.Task{Due: due}.DaysUntilDue(today)
	switch {
	case !ok:
		return bucketNone
	case days < 0:
		return bucketOverdue
	case days < 7:
		return bucketWeek
	case days < 30:
		return bucketMonth
	}
	return bucketLonger
}

// span says which dates a bucket covers, for the detail pane.
func (b bucket) span(today time.Time) string {
	day := func(n int) string { return today.AddDate(0, 0, n).Format("2006-01-02") }
	switch b {
	case bucketOverdue:
		return "before " + day(0)
	case bucketWeek:
		return day(0) + " to " + day(6)
	case bucketMonth:
		return day(7) + " to " + day(29)
	case bucketLonger:
		return "from " + day(30)
	}
	return "no due date"
}

// target names the row to select after a change: a kind and an id.
type target struct {
	kind rowKind
	id   int64
}

// editor is a form shown in place of the two panes. apply writes its
// result to the store and says which row to select afterwards.
type editor interface {
	Init() tea.Cmd
	Update(tea.Msg) (done bool, submitted bool, cmd tea.Cmd)
	View() string
	apply(*model) (target, error)
	// saved is the status line after apply succeeded.
	saved(target) string
	filtering() bool
	resize(width, height int)
	help() string
	// retry reopens a completed form with its values kept, after apply
	// failed.
	retry() tea.Cmd
}

type model struct {
	ctx   context.Context
	store *task.Store
	goals *goallink.Reader
	now   func() time.Time

	outline task.Outline
	areas   []task.Area // every area, flat, for paths and pick lists
	rows    []row
	cursor  int
	width   int
	height  int
	showAll bool     // show archived tasks and finished projects
	view    viewKind // by project or by deadline
	changed bool     // the UI has written to the database
	// collapsed holds the areas and projects whose contents are hidden in
	// the tree. It is kept across reloads, so a fold survives edits.
	collapsed map[target]bool

	// goal labels for the selected project, looked up once per project id
	goalLabels map[int64][]goallink.Goal
	goalErr    error

	mode   mode
	form   editor
	status string
	err    error
}

func newModel(ctx context.Context, store *task.Store, goals *goallink.Reader) *model {
	return &model{ctx: ctx, store: store, goals: goals, now: time.Now, width: 100, height: 30, goalLabels: map[int64][]goallink.Goal{}, collapsed: map[target]bool{}}
}

// afterWrite is reload for after a store write: it also notes that the
// database has changed, so the backup on quit knows there is something
// new to keep.
func (m *model) afterWrite() error {
	m.changed = true
	return m.reload()
}

// reload fetches the tree and rebuilds the rows, keeping the cursor on the
// same item where it still exists.
func (m *model) reload() error {
	var keep *target
	if r, ok := m.selected(); ok {
		t := r.target()
		keep = &t
	}
	outline, err := m.store.Outline(m.ctx, m.showAll)
	if err != nil {
		return err
	}
	areas, err := m.store.ListAreas(m.ctx)
	if err != nil {
		return err
	}
	m.outline, m.areas = outline, areas
	if err := m.pruneFolds(); err != nil {
		return err
	}
	m.rebuildRows()
	if keep != nil && !m.selectNear(*keep) && m.view == viewDeadline {
		// The row is gone from the list, as when a task is finished here.
		// The cursor keeps its place; if the heading of the next bucket
		// slid into it, prefer the next task, or the one before, so the
		// hand stays on tasks rather than on whatever moved up.
		m.clampCursor()
		if r, ok := m.selected(); ok && r.kind == rowHeading {
			if !m.stepToTask(1) {
				m.stepToTask(-1)
			}
		}
	}
	m.clampCursor()
	return nil
}

func (m *model) clampCursor() {
	if m.cursor >= len(m.rows) {
		m.cursor = max(0, len(m.rows)-1)
	}
}

// stepToTask moves the cursor to the next task row (dir 1) or the
// previous one (dir -1) and reports whether there was one.
func (m *model) stepToTask(dir int) bool {
	for i := m.cursor + dir; i >= 0 && i < len(m.rows); i += dir {
		if m.rows[i].kind == rowTask {
			m.cursor = i
			return true
		}
	}
	return false
}

// pruneFolds forgets folds on areas and projects that have been deleted,
// so a deleted row's fold cannot land on whatever next reuses its id. It
// checks every project, not just the ones in the outline: a finished
// project hidden by f still exists, and keeps its fold for when it shows
// again.
func (m *model) pruneFolds() error {
	if len(m.collapsed) == 0 {
		return nil
	}
	projects, err := m.store.ListProjects(m.ctx, task.ProjectFilter{All: true})
	if err != nil {
		return err
	}
	present := map[target]bool{}
	for _, a := range m.areas {
		present[target{rowArea, a.ID}] = true
	}
	for _, p := range projects {
		present[target{rowProject, p.ID}] = true
	}
	for t := range m.collapsed {
		if t.kind != rowHeading && !present[t] {
			delete(m.collapsed, t)
		}
	}
	return nil
}

// rebuildRows flattens the outline for the current view. By project, the
// rows are an area's areas, then its projects, each indented one step
// deeper, skipping what is inside a collapsed area or project. By
// deadline, the rows are every open task with its subtasks under it,
// with no area or project rows, soonest due first and undated tasks last,
// under a heading for how soon they are due, skipping what is inside a
// collapsed heading.
func (m *model) rebuildRows() {
	m.rows = m.rows[:0]
	if m.view == viewDeadline {
		m.rows = append(m.rows, m.deadlineRows()...)
		return
	}
	areas, projects := m.outline.Areas, m.outline.Projects
	var walk func(areas []task.AreaNode, projects []task.ProjectNode, depth int)
	walk = func(areas []task.AreaNode, projects []task.ProjectNode, depth int) {
		for _, a := range areas {
			m.rows = append(m.rows, row{kind: rowArea, depth: depth, area: a})
			if m.collapsed[target{rowArea, a.Area.ID}] {
				continue
			}
			walk(a.Areas, a.Projects, depth+1)
		}
		for _, p := range projects {
			m.rows = append(m.rows, row{kind: rowProject, depth: depth, project: p})
			if m.collapsed[target{rowProject, p.Project.ID}] {
				continue
			}
			for _, t := range p.Tasks {
				m.rows = append(m.rows, row{kind: rowTask, depth: depth, project: p, task: t})
				for _, s := range t.Subtasks {
					m.rows = append(m.rows, row{kind: rowSubtask, depth: depth, project: p, task: t, subtask: s})
				}
			}
		}
	}
	walk(areas, projects, 0)
}

// deadlineRows lists every open task in the outline with its subtasks,
// ordered by due date with undated tasks last, under a heading for each
// bucket that has any: overdue, the next 7 days, the next 30, longer, and
// no deadline. A collapsed heading keeps its tasks out of the rows. Done
// and dropped tasks are not coming up, so they are left out; the
// by-project view keeps them until archived. Tasks due the same day, and
// undated ones, keep their by-project order, so the list is stable across
// reloads.
func (m *model) deadlineRows() []row {
	type group struct {
		due  string
		rows []row
	}
	var groups []group
	m.eachTask(func(p task.ProjectNode, t task.TaskNode) bool {
		if t.Task.Open() {
			g := group{due: t.Task.Due, rows: []row{{kind: rowTask, project: p, task: t}}}
			for _, s := range t.Subtasks {
				g.rows = append(g.rows, row{kind: rowSubtask, project: p, task: t, subtask: s})
			}
			groups = append(groups, g)
		}
		return true
	})
	sort.SliceStable(groups, func(i, j int) bool {
		return sooner(task.Task{Due: groups[i].due}, task.Task{Due: groups[j].due})
	})
	// Groups are in bucket order, so each heading's tasks are one run.
	today := m.now()
	counts := map[bucket]int{}
	for _, g := range groups {
		counts[bucketOf(g.due, today)]++
	}
	var rows []row
	var last bucket
	for _, g := range groups {
		b := bucketOf(g.due, today)
		if b != last {
			rows = append(rows, row{kind: rowHeading, bucket: b, count: counts[b]})
			last = b
		}
		if !m.collapsed[target{rowHeading, int64(b)}] {
			rows = append(rows, g.rows...)
		}
	}
	return rows
}

func (r row) target() target {
	switch r.kind {
	case rowArea:
		return target{rowArea, r.area.Area.ID}
	case rowTask:
		return target{rowTask, r.task.Task.ID}
	case rowSubtask:
		return target{rowSubtask, r.subtask.ID}
	case rowHeading:
		return target{rowHeading, int64(r.bucket)}
	}
	return target{rowProject, r.project.Project.ID}
}

// headingOf is the by-deadline heading a task or subtask row is under.
func (m *model) headingOf(r row) target {
	return target{rowHeading, int64(bucketOf(r.task.Task.Due, m.now()))}
}

// areaOf is the area a row is in: the area itself for an area row, and
// the project's area for the rest.
func (r row) areaOf() int64 {
	if r.kind == rowArea {
		return r.area.Area.ID
	}
	return r.project.Project.AreaID
}

// areaPath names an area by its ancestry, or "" for none.
func (m *model) areaPath(id int64) string { return task.AreaPath(m.areas, id) }

// selectNear moves the cursor to the row for t, or when that row is
// folded away, as when switching back to by project from a task inside
// a folded project, to the nearest container that is listed, innermost
// first. By deadline that is the task's heading. It reports whether the
// cursor moved; a row that is gone altogether has no containers, and the
// cursor stays put.
func (m *model) selectNear(t target) bool {
	if m.selectTarget(t) {
		return true
	}
	var cs []target
	if m.view == viewDeadline {
		if h, ok := m.deadlineContainer(t); ok {
			cs = []target{h}
		}
	} else {
		cs = m.containers(t)
	}
	for i := len(cs) - 1; i >= 0; i-- {
		if m.selectTarget(cs[i]) {
			return true
		}
	}
	return false
}

// selectTarget moves the cursor to the row for t and reports whether it
// found one. When t is gone, for instance because it was just hidden, the
// cursor stays where it is.
func (m *model) selectTarget(t target) bool {
	for i, r := range m.rows {
		if r.target() == t {
			m.cursor = i
			return true
		}
	}
	return false
}

func (m *model) selected() (row, bool) {
	if len(m.rows) == 0 || m.cursor >= len(m.rows) {
		return row{}, false
	}
	return m.rows[m.cursor], true
}

// projectGoals returns the labelled goals for a project, looking them up
// the first time. Without a reader, or when goaltracker's database cannot
// be read, the labels are bare ids.
func (m *model) projectGoals(p task.Project) []goallink.Goal {
	if g, ok := m.goalLabels[p.ID]; ok {
		return g
	}
	var goals []goallink.Goal
	if m.goals == nil {
		for _, id := range p.GoalIDs {
			goals = append(goals, goallink.Goal{ID: id})
		}
	} else {
		var err error
		goals, err = m.goals.Lookup(m.ctx, p.GoalIDs)
		if err != nil {
			m.goalErr = err
		}
	}
	m.goalLabels[p.ID] = goals
	return goals
}

func (m *model) Init() tea.Cmd { return nil }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.mode == modeForm && m.form != nil {
			m.form.resize(m.formSize())
		}
		return m, nil
	case tea.KeyPressMsg:
		switch m.mode {
		case modeConfirmDelete:
			return m.updateConfirm(msg)
		case modeConfirmState:
			return m.updateConfirmState(msg)
		case modeForm:
			return m.updateForm(msg)
		}
		return m.updateBrowse(msg)
	}
	if m.mode == modeForm {
		return m.updateForm(msg)
	}
	return m, nil
}

// footer renders the status and help lines wrapped to the terminal width.
func (m *model) footer() string {
	wrap := lipgloss.NewStyle().Width(max(10, m.width))
	return wrap.Render(m.viewStatus()) + "\n" + wrap.Render(dimStyle.Render(m.helpLine()))
}

// bodyHeight is the height of the main panes: everything but the title and
// the footer, which can wrap on narrow terminals.
func (m *model) bodyHeight() int {
	return max(5, m.height-1-lipgloss.Height(m.footer()))
}

// formSize is the content area inside the form's pane.
func (m *model) formSize() (int, int) { return m.width - 4, m.bodyHeight() - 2 }

// openEditor shows an editor in place of the panes. It is sized once it is
// in place, since formSize measures the editor's own help line.
func (m *model) openEditor(e editor) tea.Cmd {
	m.mode = modeForm
	m.form = e
	e.resize(m.formSize())
	return e.Init()
}

func (m *model) updateForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "esc" && !m.form.filtering() {
		m.mode = modeBrowse
		m.form = nil
		m.status = "cancelled"
		return m, nil
	}
	done, submitted, cmd := m.form.Update(msg)
	if !done {
		return m, cmd
	}
	m.mode = modeBrowse
	if !submitted {
		m.status = "cancelled"
		m.form = nil
		return m, nil
	}
	t, err := m.form.apply(m)
	if err != nil {
		// Keep the form, and what was typed, so the user can fix it or
		// cancel with esc. The form has completed, so it must be put back
		// to work before it takes input again.
		m.err = err
		m.mode = modeForm
		return m, m.form.retry()
	}
	form := m.form
	m.form = nil
	// A project's goal links may have changed; look them up afresh.
	delete(m.goalLabels, t.id)
	if err := m.afterWrite(); err != nil {
		m.err = err
		return m, nil
	}
	m.reveal(t)
	m.status = form.saved(t)
	if m.view == viewDeadline && (t.kind == rowArea || t.kind == rowProject) {
		// Areas and projects have no row by deadline, so say where it went.
		m.status += " (v shows it by project)"
	}
	return m, nil
}

func (t target) label() string {
	switch t.kind {
	case rowArea:
		return fmt.Sprintf("area #%d", t.id)
	case rowTask:
		return fmt.Sprintf("task #%d", t.id)
	case rowSubtask:
		return fmt.Sprintf("subtask #%d", t.id)
	}
	return fmt.Sprintf("project #%d", t.id)
}

func (m *model) updateBrowse(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m.status, m.err = "", nil
	switch msg.String() {
	case "q", "ctrl+c", "esc":
		return m, tea.Quit
	case "j", "down":
		if m.cursor < len(m.rows)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		m.cursor = max(0, len(m.rows)-1)
	case "r":
		m.goalLabels = map[int64][]goallink.Goal{}
		m.goalErr = nil
		m.err = m.reload()
		m.status = "reloaded"
	case "f":
		m.showAll = !m.showAll
		m.err = m.reload()
		if m.showAll {
			m.status = "showing archived tasks and finished projects"
		} else {
			m.status = "hiding archived tasks and finished projects"
		}
		if m.view == viewDeadline {
			// Finished tasks are never listed here, so what f changes is
			// whether open tasks in finished projects are.
			if m.showAll {
				m.status += "; here that adds open tasks in finished projects"
			} else {
				m.status += "; here that drops open tasks in finished projects"
			}
		}
	case "v":
		from, _ := m.selected()
		if m.view == viewTree {
			m.view = viewDeadline
			m.status = "by deadline: open tasks under overdue, next 7 days, next 30 days, longer, no deadline"
		} else {
			m.view = viewTree
			m.status = "by project"
		}
		m.err = m.reload()
		if m.view == viewDeadline {
			m.landByDeadline(from)
		} else {
			m.landByProject(from)
		}
	case "left", "right":
		m.toggleFold()
	case "space", " ", "enter":
		m.advance()
	case "x":
		m.drop()
	case "z":
		m.archive()
	case "d":
		if r, ok := m.selected(); ok {
			if r.kind == rowHeading {
				m.status = headingHint
				return m, nil
			}
			m.mode = modeConfirmDelete
		}
	case "n":
		return m, m.openEditor(newAreaForm(nil, m.areaHere(), m.areas, nil, 0, 0))
	case "A":
		return m, m.openEditor(newProjectForm(nil, m.areaHere(), m.areas, m.goalOptions(), 0, 0))
	case "a":
		r, ok := m.selected()
		if !ok {
			if m.view == viewDeadline {
				m.status = "no task to add under; v goes back to by project, where a adds one under a project"
			} else {
				m.status = "add a project first: press A"
			}
			return m, nil
		}
		switch r.kind {
		case rowArea:
			m.status = "select a project to add a task under; A adds one here"
			return m, nil
		case rowHeading:
			m.status = "select a task to add one under its project"
			return m, nil
		}
		return m, m.openEditor(newTaskForm(nil, r.project.Project.ID, m.projectOptions(), m.now(), 0, 0))
	case "s":
		r, ok := m.selected()
		if !ok || r.kind == rowArea || r.kind == rowProject || r.kind == rowHeading {
			m.status = "select a task to add a subtask under"
			return m, nil
		}
		return m, m.openEditor(newSubtaskForm(nil, r.task.Task, 0, 0))
	case "c":
		r, ok := m.selected()
		if !ok || r.kind == rowArea || r.kind == rowProject || r.kind == rowHeading {
			m.status = "select a task to copy"
			return m, nil
		}
		t := r.task.Task
		return m, m.openEditor(newCopyForm(t, len(r.task.Subtasks), m.projectOptions(), m.now(), 0, 0))
	case "e":
		r, ok := m.selected()
		if !ok {
			return m, nil
		}
		switch r.kind {
		case rowHeading:
			m.status = headingHint
		case rowArea:
			a := r.area.Area
			return m, m.openEditor(newAreaForm(&a, a.ParentID, m.areas, inside(r.area), 0, 0))
		case rowProject:
			p := r.project.Project
			return m, m.openEditor(newProjectForm(&p, p.AreaID, m.areas, m.goalOptions(), 0, 0))
		case rowTask:
			t := r.task.Task
			return m, m.openEditor(newTaskForm(&t, t.ProjectID, m.projectOptions(), m.now(), 0, 0))
		case rowSubtask:
			s := r.subtask
			return m, m.openEditor(newSubtaskForm(&s, r.task.Task, 0, 0))
		}
	}
	return m, nil
}

// landByDeadline moves the cursor after switching to by deadline from a
// row that has no row there: an area, a project, or a finished task or
// subtask, which the list leaves out. It goes to the soonest due task
// listed from the same area or project; when none is listed, to the
// heading that hides the first such task; and to the top when there is
// no such task at all. From an open task or subtask the cursor is
// already on the same row.
func (m *model) landByDeadline(from row) {
	within := map[int64]bool{}
	switch from.kind {
	case rowTask, rowSubtask:
		if _, ok := m.deadlineContainer(from.target()); ok {
			// An open task is listed here, or its heading is: reload has
			// put the cursor on one or the other.
			return
		}
		within[from.project.Project.ID] = true
	case rowProject:
		within[from.project.Project.ID] = true
	case rowArea:
		var walk func(n task.AreaNode)
		walk = func(n task.AreaNode) {
			for _, p := range n.Projects {
				within[p.Project.ID] = true
			}
			for _, a := range n.Areas {
				walk(a)
			}
		}
		walk(from.area)
	default:
		return
	}
	for i, r := range m.rows {
		if r.kind == rowTask && within[r.project.Project.ID] {
			m.cursor = i
			return
		}
	}
	// Nothing listed: every such task is under a folded heading. Land on
	// the heading of the soonest due one, as the listed case would.
	var soonest *task.Task
	m.eachTask(func(p task.ProjectNode, tk task.TaskNode) bool {
		if tk.Task.Open() && within[p.Project.ID] && (soonest == nil || sooner(tk.Task, *soonest)) {
			t := tk.Task
			soonest = &t
		}
		return true
	})
	if soonest == nil || !m.selectNear(target{rowTask, soonest.ID}) {
		m.cursor = 0
	}
}

// sooner reports whether a is due before b, with an undated task after
// any dated one.
func sooner(a, b task.Task) bool {
	if a.Due == "" || b.Due == "" {
		return a.Due != "" && b.Due == ""
	}
	return a.Due < b.Due
}

// landByProject moves the cursor after switching to by project from a
// heading, which has no row there: to the first task listed that is due
// about as soon, or to the top when none is.
func (m *model) landByProject(from row) {
	if from.kind != rowHeading {
		return
	}
	var first *task.Task
	m.eachTask(func(_ task.ProjectNode, tk task.TaskNode) bool {
		if tk.Task.Open() && bucketOf(tk.Task.Due, m.now()) == from.bucket {
			first = &tk.Task
			return false
		}
		return true
	})
	if first == nil || !m.selectNear(target{rowTask, first.ID}) {
		m.cursor = 0
	}
}

// eachTask calls fn for every task in the outline, in by-project order,
// until fn returns false.
func (m *model) eachTask(fn func(p task.ProjectNode, tk task.TaskNode) bool) {
	var walk func(areas []task.AreaNode, projects []task.ProjectNode) bool
	walk = func(areas []task.AreaNode, projects []task.ProjectNode) bool {
		for _, a := range areas {
			if !walk(a.Areas, a.Projects) {
				return false
			}
		}
		for _, p := range projects {
			for _, tk := range p.Tasks {
				if !fn(p, tk) {
					return false
				}
			}
		}
		return true
	}
	walk(m.outline.Areas, m.outline.Projects)
}

// findTask is the task t names, or the task a subtask t is under.
func (m *model) findTask(t target) (task.Task, bool) {
	var found *task.Task
	m.eachTask(func(_ task.ProjectNode, tk task.TaskNode) bool {
		if t.kind == rowTask && tk.Task.ID == t.id {
			found = &tk.Task
			return false
		}
		if t.kind == rowSubtask {
			for _, s := range tk.Subtasks {
				if s.ID == t.id {
					found = &tk.Task
					return false
				}
			}
		}
		return true
	})
	if found == nil {
		return task.Task{}, false
	}
	return *found, true
}

// headingHint is what a heading says to keys that act on a task, project
// or area.
const headingHint = "headings group tasks by how soon they are due; ←/→ folds one, e and d act on tasks"

// areaHere is the area a new area or project goes in by default: the
// selected row's area, or the top when nothing is selected or the
// selection is a by-deadline heading, which is in no area.
func (m *model) areaHere() int64 {
	if r, ok := m.selected(); ok {
		return r.areaOf()
	}
	return 0
}

// inside lists an area and every area within it, which an area cannot be
// moved into.
func inside(n task.AreaNode) map[int64]bool {
	out := map[int64]bool{n.Area.ID: true}
	for _, a := range n.Areas {
		for id := range inside(a) {
			out[id] = true
		}
	}
	return out
}

// toggleFold is the left or right arrow: it hides what is inside the
// selected area, project or by-deadline heading, or shows it again when
// it is hidden. On a task or subtask it folds the project the row is in,
// or by deadline the heading it is under, and moves the cursor there.
func (m *model) toggleFold() {
	r, ok := m.selected()
	if !ok {
		return
	}
	var (
		t     target
		name  string
		held  string // what folding hides, for the status line
		empty string // why there is nothing to hide, or "" when there is
	)
	switch {
	case r.kind == rowArea:
		t, name, held = r.target(), r.area.Area.Name, "what is in it"
		if len(r.area.Areas)+len(r.area.Projects) == 0 {
			empty = "the area is empty"
		}
	case r.kind == rowHeading:
		t, name, held = r.target(), r.bucket.String(), "its tasks"
	case m.view == viewDeadline:
		t, name, held = m.headingOf(r), bucketOf(r.task.Task.Due, m.now()).String(), "its tasks"
	default:
		t, name, held = target{rowProject, r.project.Project.ID}, r.project.Project.Name, "its tasks"
		if len(r.project.Tasks) == 0 {
			empty = "the project has no tasks listed"
		}
	}
	switch {
	case m.collapsed[t] && r.target() == t:
		// Unfold before asking whether there is anything to hide, so a fold
		// whose contents have since gone can still be undone.
		delete(m.collapsed, t)
		m.status = "expanded " + name
	case empty != "":
		m.status = "nothing to hide: " + empty
		return
	default:
		m.collapsed[t] = true
		m.status = "collapsed " + name + "; ← shows " + held + " again"
	}
	m.rebuildRows()
	m.selectTarget(t)
}

// reveal selects t, first expanding every collapsed area and project
// around it, so a row folded away can still be shown. By deadline that
// is the heading the task is under; the tree's folds are left as they
// are.
func (m *model) reveal(t target) {
	var around []target
	if m.view == viewDeadline {
		if h, ok := m.deadlineContainer(t); ok {
			around = []target{h}
		}
	} else {
		around = m.containers(t)
	}
	changed := false
	for _, c := range around {
		if m.collapsed[c] {
			delete(m.collapsed, c)
			changed = true
		}
	}
	if changed {
		m.rebuildRows()
	}
	m.selectTarget(t)
}

// deadlineContainer is the heading a task or subtask is under by
// deadline. ok is false for anything else, and for a finished task, which
// is not listed there.
func (m *model) deadlineContainer(t target) (target, bool) {
	if t.kind != rowTask && t.kind != rowSubtask {
		return target{}, false
	}
	found, ok := m.findTask(t)
	if !ok || !found.Open() {
		return target{}, false
	}
	return target{rowHeading, int64(bucketOf(found.Due, m.now()))}, true
}

// containers lists what t is inside, outermost first: the areas around
// it, and for a task or subtask its project. Rows that are hidden count,
// since this is how they are found again.
func (m *model) containers(t target) []target {
	var walk func(areas []task.AreaNode, projects []task.ProjectNode, path []target) ([]target, bool)
	walk = func(areas []task.AreaNode, projects []task.ProjectNode, path []target) ([]target, bool) {
		for _, a := range areas {
			if t.kind == rowArea && a.Area.ID == t.id {
				return path, true
			}
			if found, ok := walk(a.Areas, a.Projects, append(path, target{rowArea, a.Area.ID})); ok {
				return found, true
			}
		}
		for _, p := range projects {
			if t.kind == rowProject && p.Project.ID == t.id {
				return path, true
			}
			for _, tk := range p.Tasks {
				if t.kind == rowTask && tk.Task.ID == t.id {
					return append(path, target{rowProject, p.Project.ID}), true
				}
				for _, s := range tk.Subtasks {
					if t.kind == rowSubtask && s.ID == t.id {
						return append(path, target{rowProject, p.Project.ID}), true
					}
				}
			}
		}
		return nil, false
	}
	found, _ := walk(m.outline.Areas, m.outline.Projects, nil)
	return found
}

// advance is the space bar: a task steps todo, doing, done; a subtask
// toggles its tick; a project steps active, done, shelved. An area has
// no state to step.
func (m *model) advance() {
	r, ok := m.selected()
	if !ok {
		return
	}
	switch r.kind {
	case rowArea:
		m.status = "areas have no state; e renames, d deletes"
		return
	case rowHeading:
		m.status = headingHint
		return
	case rowSubtask:
		done := !r.subtask.Done
		if err := m.store.TickSubtask(m.ctx, r.subtask.ID, done); err != nil {
			m.err = err
			return
		}
		if done {
			m.status = fmt.Sprintf("ticked subtask #%d", r.subtask.ID)
		} else {
			m.status = fmt.Sprintf("unticked subtask #%d", r.subtask.ID)
		}
	case rowTask:
		next := r.task.Task.Status.Next()
		if err := m.store.MarkTask(m.ctx, r.task.Task.ID, next); err != nil {
			m.err = err
			return
		}
		m.status = fmt.Sprintf("task #%d %s", r.task.Task.ID, next)
		switch {
		case r.task.Task.Archived:
			// An archived task shown with f: stepping it back to todo
			// reopens it, which takes it out of the archive.
			m.status += " (back from the archive)"
		case next == task.Finished:
			m.status += m.finishedHint()
		}
	case rowProject:
		next := nextState(r.project.Project.State)
		if next != task.Active {
			// A done or shelved project is hidden with everything in it,
			// which is too much to do on one stray keypress.
			m.mode = modeConfirmState
			return
		}
		m.setProjectState(r.project.Project.ID, next)
		return
	}
	m.err = m.afterWrite()
}

func (m *model) setProjectState(id int64, next task.State) {
	if err := m.store.MarkProject(m.ctx, id, next); err != nil {
		m.err = err
		return
	}
	m.status = fmt.Sprintf("project #%d %s", id, next)
	if next != task.Active {
		m.status += m.hiddenHint("finished")
	}
	m.err = m.afterWrite()
}

func nextState(s task.State) task.State {
	switch s {
	case task.Active:
		return task.Done
	case task.Done:
		return task.Shelved
	}
	return task.Active
}

// drop is x: a task is dropped, a project shelved. A dropped task stays
// in the tree, greyed, until it is archived; a shelved project is hidden
// unless finished things are shown. Pressing it again on a dropped task
// or a shelved project brings it back.
func (m *model) drop() {
	r, ok := m.selected()
	if !ok {
		return
	}
	switch r.kind {
	case rowTask:
		st := task.Dropped
		if r.task.Task.Status == task.Dropped {
			st = task.Todo
		}
		if err := m.store.MarkTask(m.ctx, r.task.Task.ID, st); err != nil {
			m.err = err
			return
		}
		m.status = fmt.Sprintf("task #%d %s", r.task.Task.ID, st)
		switch {
		case r.task.Task.Archived && st == task.Dropped:
			// An archived done task shown with f: it stays archived.
			m.status += " (still archived)"
		case r.task.Task.Archived:
			// An archived dropped task shown with f: reopening it takes
			// it out of the archive.
			m.status += " (back from the archive)"
		case st == task.Dropped:
			m.status += m.finishedHint()
		}
	case rowProject:
		st := task.Shelved
		if r.project.Project.State == task.Shelved {
			st = task.Active
		}
		if err := m.store.MarkProject(m.ctx, r.project.Project.ID, st); err != nil {
			m.err = err
			return
		}
		m.status = fmt.Sprintf("project #%d %s", r.project.Project.ID, st) + m.hiddenHint("finished")
	case rowArea:
		m.status = "areas have no state; d deletes one and moves what is in it up a level"
		return
	case rowHeading:
		m.status = headingHint
		return
	default:
		m.status = "subtasks are ticked with space, or deleted with d"
		return
	}
	m.err = m.afterWrite()
}

// archive is z: a finished task is put away, out of the tree, and an
// archived task is brought back. An open task cannot be archived, since
// it is still work to do.
func (m *model) archive() {
	r, ok := m.selected()
	if !ok {
		return
	}
	switch r.kind {
	case rowArea, rowProject:
		m.status = "only tasks are archived; space or x on a project finishes it, which hides it"
		return
	case rowHeading:
		m.status = headingHint
		return
	case rowSubtask:
		m.status = "subtasks go with their task; z on the task archives it"
		return
	}
	t := r.task.Task
	switch {
	case t.Archived:
		if err := m.store.ArchiveTask(m.ctx, t.ID, false); err != nil {
			m.err = err
			return
		}
		m.status = fmt.Sprintf("task #%d back from the archive", t.ID)
	case t.Open():
		where := ""
		if m.view == viewDeadline {
			where = " in by project" // a finished task leaves this list
		}
		m.status = fmt.Sprintf("task #%d is still %s; space finishes it or x drops it, then z%s archives it", t.ID, t.Status, where)
		return
	default:
		if err := m.store.ArchiveTask(m.ctx, t.ID, true); err != nil {
			m.err = err
			return
		}
		m.status = fmt.Sprintf("task #%d archived", t.ID) + m.hiddenHint("archived")
	}
	m.err = m.afterWrite()
}

// finishedHint follows a task being done or dropped: it stays listed
// until archived.
func (m *model) finishedHint() string {
	if m.view == viewDeadline {
		return " (gone from this list; by project shows it until archived)"
	}
	return "; z archives it"
}

// hiddenHint explains where something just put out of sight went: what
// is "archived" for a task or "finished" for a project.
func (m *model) hiddenHint(what string) string {
	if !m.showAll {
		return " (hidden; f shows " + what + ")"
	}
	return ""
}

func (m *model) updateConfirm(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	r, _ := m.selected()
	m.mode = modeBrowse
	switch msg.String() {
	case "y", "Y":
		var err error
		switch r.kind {
		case rowArea:
			err = m.store.DeleteArea(m.ctx, r.area.Area.ID)
		case rowProject:
			err = m.store.DeleteProject(m.ctx, r.project.Project.ID)
		case rowTask:
			err = m.store.DeleteTask(m.ctx, r.task.Task.ID)
		case rowSubtask:
			err = m.store.DeleteSubtask(m.ctx, r.subtask.ID)
		}
		if err != nil {
			m.err = err
			return m, nil
		}
		m.status = fmt.Sprintf("deleted %s", r.target().label())
		m.err = m.afterWrite()
	default:
		m.status = "kept"
	}
	return m, nil
}

func (m *model) updateConfirmState(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	r, _ := m.selected()
	m.mode = modeBrowse
	switch msg.String() {
	case "y", "Y":
		m.setProjectState(r.project.Project.ID, nextState(r.project.Project.State))
	default:
		m.status = "kept"
	}
	return m, nil
}

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	selectedStyle = lipgloss.NewStyle().Reverse(true)
	doneStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	doingStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	overdueStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	labelStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	projectStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	areaStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))
	headingStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4"))
	errStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	paneStyle     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8")).Padding(0, 1)
)

func (m *model) View() tea.View {
	listW := m.width * 55 / 100
	detailW := m.width - listW
	bodyH := m.bodyHeight()

	// paneStyle's Width and Height include its border and padding, so the
	// content area is 4 narrower (border 2 + padding 2) and 2 shorter.
	left := paneStyle.Width(listW).Height(bodyH).Render(m.viewList(listW-4, bodyH-2))
	right := paneStyle.Width(detailW).Height(bodyH).Render(clipLines(m.viewDetail(detailW-4), bodyH-2))

	var b strings.Builder
	head := titleStyle.Render("tasktracker · " + m.view.String())
	if m.showAll {
		head += dimStyle.Render(" · showing archived")
	}
	b.WriteString(ansi.Truncate(head, max(10, m.width), "…"))
	b.WriteString("\n")
	if m.mode == modeForm && m.form != nil {
		b.WriteString(paneStyle.Width(m.width).Height(bodyH).Render(clipLines(m.form.View(), bodyH-2)))
	} else {
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, left, right))
	}
	b.WriteString("\n")
	b.WriteString(m.footer())

	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

// clipLines keeps the first h lines of s, ending with a marker when
// anything was cut, so the pane never grows past its height.
func clipLines(s string, h int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= h {
		return s
	}
	if h < 1 {
		return ""
	}
	return strings.Join(lines[:h-1], "\n") + "\n" + dimStyle.Render("…")
}

func (m *model) viewList(w, h int) string {
	if len(m.rows) == 0 {
		switch {
		case m.view == viewDeadline:
			return dimStyle.Render("no open tasks\n\nv goes back to by project")
		case !m.showAll:
			return dimStyle.Render("no projects yet\n\nA adds one; f shows finished projects")
		default:
			return dimStyle.Render("no projects yet\n\nA adds one")
		}
	}
	var lines []string
	for i, r := range m.rows {
		lines = append(lines, m.viewRow(r, i == m.cursor, w))
	}
	start := 0
	if m.cursor >= h {
		start = m.cursor - h + 1
	}
	return strings.Join(lines[start:min(len(lines), start+h)], "\n")
}

// statusGlyph is the mark in front of a task.
func statusGlyph(s task.Status) string {
	switch s {
	case task.Doing:
		return "◐"
	case task.Finished:
		return "●"
	case task.Dropped:
		return "✗"
	}
	return "○"
}

// viewRow renders one row as a line of width w. The row is built as plain
// text first so width and truncation are measured without escape codes,
// then styled: finished things are dimmed as a whole, an overdue date is
// red, and by deadline the project name is dimmed after the title.
func (m *model) viewRow(r row, selected bool, w int) string {
	var left, right, suffix string
	finished := false
	indent := strings.Repeat("  ", r.depth)
	switch r.kind {
	case rowHeading:
		left = m.foldMark(r.target()) + r.bucket.String()
		if r.count == 1 {
			right = "1 task"
		} else {
			right = fmt.Sprintf("%d tasks", r.count)
		}
	case rowArea:
		left = indent + m.foldMark(r.target()) + r.area.Area.Name
		if n := r.area.OpenTasks(); n > 0 {
			right = fmt.Sprintf("%d open", n)
		}
	case rowProject:
		p := r.project.Project
		left = indent + m.foldMark(r.target()) + p.Name
		if n := r.project.OpenTasks(); n > 0 {
			right = fmt.Sprintf("%d open", n)
		}
		if p.State != task.Active {
			right = string(p.State)
			finished = true
		}
	case rowTask:
		t := r.task.Task
		indent += "  "
		if m.view == viewDeadline {
			indent = ""
			suffix = "  " + r.project.Project.Name
		}
		left = fmt.Sprintf("%s%s %s", indent, statusGlyph(t.Status), t.Title)
		var parts []string
		if n := len(r.task.Subtasks); n > 0 {
			parts = append(parts, fmt.Sprintf("%d/%d", r.task.Ticked(), n))
		}
		if t.Notes != "" {
			parts = append(parts, "≡")
		}
		if t.Archived {
			parts = append(parts, "archived")
		}
		if t.Due != "" {
			parts = append(parts, t.Due)
		}
		right = strings.Join(parts, "  ")
		finished = !t.Open()
	case rowSubtask:
		box := "[ ]"
		if r.subtask.Done {
			box = "[x]"
		}
		indent += "    "
		if m.view == viewDeadline {
			indent = "  "
		}
		left = fmt.Sprintf("%s%s %s", indent, box, r.subtask.Title)
	}
	line := fit(left+suffix, right, w)
	switch {
	case selected:
		return selectedStyle.Render(line)
	case finished:
		return dimStyle.Render(line)
	case r.kind == rowArea:
		return areaStyle.Render(line)
	case r.kind == rowHeading:
		return headingStyle.Render(line)
	case r.kind == rowProject:
		return projectStyle.Render(line)
	}
	// Style parts of the line by position, never by searching for text
	// that a title could also contain.
	head, tail := line[:len(line)-len(right)], line[len(line)-len(right):]
	if suffix != "" && strings.HasPrefix(head, left) {
		// fit may have cut the suffix short: dim whatever of it is visible,
		// from the end of left to the end of the text, leaving the padding.
		text := strings.TrimRight(head, " ")
		if len(text) > len(left) {
			head = left + dimStyle.Render(text[len(left):]) + head[len(text):]
		}
	}
	if r.kind == rowTask {
		t := r.task.Task
		if t.Overdue(m.now()) && strings.HasSuffix(tail, t.Due) {
			tail = tail[:len(tail)-len(t.Due)] + overdueStyle.Render(t.Due)
		}
		if t.Status == task.Doing {
			head = strings.Replace(head, "◐", doingStyle.Render("◐"), 1)
		}
	}
	return head + tail
}

// foldMark is the glyph in front of an area or project: pointing down
// when its contents are listed below it, right when they are folded away.
func (m *model) foldMark(t target) string {
	if m.collapsed[t] {
		return "▸ "
	}
	return "▾ "
}

// fit pads or truncates left so that right sits flush at width w. Both the
// measurement and the cut use display width, so wide characters count as
// two columns.
func fit(left, right string, w int) string {
	avail := max(0, w-lipgloss.Width(right))
	left = ansi.Truncate(left, avail, "…")
	pad := max(0, avail-lipgloss.Width(left))
	return left + strings.Repeat(" ", pad) + right
}

// viewDetail renders the selected row w wide, as many lines as it takes;
// the caller clips it to the pane with clipLines.
func (m *model) viewDetail(w int) string {
	r, ok := m.selected()
	if !ok {
		return ""
	}
	wrap := lipgloss.NewStyle().Width(w)
	cut := func(line string) string { return ansi.Truncate(line, w, "…") }
	label := func(name, value string) string { return cut(labelStyle.Render(name) + " " + value) }
	var lines []string
	switch r.kind {
	case rowHeading:
		lines = append(lines, strings.Split(wrap.Bold(true).Render(r.bucket.String()), "\n")...)
		lines = append(lines, label("due  ", r.bucket.span(m.now())))
		lines = append(lines, label("tasks", fmt.Sprintf("%d open", r.count)))
		if m.collapsed[r.target()] {
			lines = append(lines, "", dimStyle.Render("collapsed; ← shows its tasks"))
		}
	case rowArea:
		a := r.area.Area
		lines = append(lines, strings.Split(wrap.Bold(true).Render(a.Name), "\n")...)
		if a.ParentID != 0 {
			lines = append(lines, label("in     ", m.areaPath(a.ParentID)))
		}
		areas, projects := r.area.Counts()
		lines = append(lines, label("holds  ", fmt.Sprintf("%d areas, %d projects   ", areas, projects)+labelStyle.Render("id")+fmt.Sprintf(" #%d", a.ID)))
		lines = append(lines, label("tasks  ", fmt.Sprintf("%d open", r.area.OpenTasks())))
		if m.collapsed[r.target()] {
			lines = append(lines, "", dimStyle.Render("collapsed; ← shows what is in it"))
		}
	case rowProject:
		p := r.project.Project
		lines = append(lines, strings.Split(wrap.Bold(true).Render(p.Name), "\n")...)
		if p.AreaID != 0 {
			lines = append(lines, label("area ", m.areaPath(p.AreaID)))
		}
		lines = append(lines, label("state", string(p.State)+"   "+labelStyle.Render("id")+fmt.Sprintf(" #%d", p.ID)))
		lines = append(lines, label("tasks", fmt.Sprintf("%d open, %d listed", r.project.OpenTasks(), len(r.project.Tasks))))
		if m.collapsed[r.target()] {
			lines = append(lines, "", dimStyle.Render("collapsed; ← shows its tasks"))
		}
		if p.Description != "" {
			lines = append(lines, "", labelStyle.Render("about"))
			lines = append(lines, strings.Split(wrap.Render(p.Description), "\n")...)
		}
		if goals := m.projectGoals(p); len(goals) > 0 {
			lines = append(lines, "", labelStyle.Render("goals"))
			for _, g := range goals {
				lines = append(lines, cut("  "+g.Label()))
			}
			if m.goalErr != nil {
				lines = append(lines, cut(dimStyle.Render("  (goaltracker not readable)")))
			}
		}
	case rowTask:
		t := r.task.Task
		lines = append(lines, strings.Split(wrap.Bold(true).Render(t.Title), "\n")...)
		lines = append(lines, label("project", r.project.Project.Name))
		status := statusText(t.Status)
		if t.Archived {
			status += " · archived"
		}
		lines = append(lines, label("status ", status+"   "+labelStyle.Render("id")+fmt.Sprintf(" #%d", t.ID)))
		if t.Due != "" {
			due := t.Due
			if days, ok := t.DaysUntilDue(m.now()); ok && t.Open() {
				due += "  " + dueWords(days)
			}
			if t.Overdue(m.now()) {
				due = overdueStyle.Render(due)
			}
			lines = append(lines, label("due    ", due))
		}
		if t.Issue != "" {
			// A recognised link is shown short, as an OSC 8 hyperlink that
			// a terminal which knows them opens on a click. Anything else,
			// as from a hand-edited database, is shown as IssueText makes
			// it safe to print.
			issue := t.IssueText()
			if ref, link, ok := t.IssueRef(); ok {
				issue = lipgloss.NewStyle().Hyperlink(link).Render(ref)
			}
			lines = append(lines, label("issue  ", issue))
		}
		if len(r.task.Subtasks) > 0 {
			lines = append(lines, "", labelStyle.Render(fmt.Sprintf("subtasks %d/%d", r.task.Ticked(), len(r.task.Subtasks))))
			for _, s := range r.task.Subtasks {
				box := "[ ]"
				if s.Done {
					box = "[x]"
				}
				lines = append(lines, cut("  "+box+" "+s.Title))
			}
		}
		if t.Notes != "" {
			lines = append(lines, "", labelStyle.Render("notes"))
			lines = append(lines, strings.Split(wrap.Render(t.Notes), "\n")...)
		}
	case rowSubtask:
		s := r.subtask
		lines = append(lines, strings.Split(wrap.Bold(true).Render(s.Title), "\n")...)
		lines = append(lines, label("under  ", r.task.Task.Title))
		lines = append(lines, label("project", r.project.Project.Name))
		state := "not yet"
		if s.Done {
			state = "ticked"
		}
		lines = append(lines, label("done   ", state+"   "+labelStyle.Render("id")+fmt.Sprintf(" #%d", s.ID)))
	}
	// clipLines marks what it cuts, so notes under a long checklist are
	// seen to be cut rather than missing.
	return strings.Join(lines, "\n")
}

func statusText(s task.Status) string {
	switch s {
	case task.Finished:
		return doneStyle.Render("done")
	case task.Doing:
		return doingStyle.Render("doing")
	case task.Dropped:
		return dimStyle.Render("dropped")
	}
	return "todo"
}

// dueWords says how far off a due date is.
func dueWords(days int) string {
	switch {
	case days < -1:
		return fmt.Sprintf("%d days overdue", -days)
	case days == -1:
		return "1 day overdue"
	case days == 0:
		return "today"
	case days == 1:
		return "tomorrow"
	}
	return fmt.Sprintf("in %d days", days)
}

func (m *model) viewStatus() string {
	switch {
	case m.err != nil:
		return errStyle.Render("error: " + m.err.Error())
	case m.mode == modeConfirmDelete:
		r, _ := m.selected()
		switch r.kind {
		case rowArea:
			return errStyle.Render(fmt.Sprintf("delete area #%d %q and move what is in it up a level? y/N", r.area.Area.ID, r.area.Area.Name))
		case rowProject:
			return errStyle.Render(fmt.Sprintf("delete project #%d %q and every task in it? y/N", r.project.Project.ID, r.project.Project.Name))
		case rowTask:
			return errStyle.Render(fmt.Sprintf("delete task #%d %q and its subtasks? y/N", r.task.Task.ID, r.task.Task.Title))
		default:
			return errStyle.Render(fmt.Sprintf("delete subtask #%d %q? y/N", r.subtask.ID, r.subtask.Title))
		}
	case m.mode == modeConfirmState:
		r, _ := m.selected()
		p := r.project.Project
		verb := "mark done"
		if nextState(p.State) == task.Shelved {
			verb = "shelve"
		}
		return errStyle.Render(fmt.Sprintf("%s project #%d %q and hide it with every task in it? y/N", verb, p.ID, p.Name))
	}
	return m.status
}

func (m *model) helpLine() string {
	if m.mode == modeForm && m.form != nil {
		return m.form.help()
	}
	keys := "n area · A project · a task · s subtask · c copy task · e edit · space next status/tick · x drop · z archive · d delete · f show archived"
	if m.view == viewDeadline {
		return keys + " · v by project · ←/→ fold/unfold · j/k move · q quit"
	}
	return keys + " · v by deadline · ←/→ fold/unfold · j/k move · q quit"
}
