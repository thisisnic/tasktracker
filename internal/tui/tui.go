// Package tui is the terminal UI for tasktracker, built on Bubble Tea v2.
//
// The left pane is a tree of areas, projects, tasks and subtasks, or a flat
// list of tasks with due dates. A project in the tree can be collapsed to
// hide its tasks, and an area to hide everything in it. The right pane
// shows the selected item. Forms for adding and editing take over both
// panes.
package tui

import (
	"context"
	"fmt"
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
	// ShowFinished starts with done and dropped tasks and finished projects
	// visible.
	ShowFinished bool
}

// Run opens the tree view and blocks until the user quits.
func Run(ctx context.Context, store *task.Store, opts Options) error {
	m := newModel(ctx, store, opts.Goals)
	m.showAll = opts.ShowFinished
	if err := m.reload(); err != nil {
		return err
	}
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	return err
}

type mode int

const (
	modeBrowse mode = iota
	modeConfirmDelete
	modeConfirmState // space on a project: leaving active hides it, so ask first
	modeForm
)

type viewKind int

const (
	viewTree viewKind = iota
	viewDue
)

// rowKind says what a row in the left pane stands for.
type rowKind int

const (
	rowArea rowKind = iota
	rowProject
	rowTask
	rowSubtask
)

// row is one selectable line in the left pane. Area is set for area rows.
// Project is set for every other row: the project the row belongs to. Task
// is set for task and subtask rows. Depth is how many areas the row is
// inside, and sets its indent.
type row struct {
	kind    rowKind
	depth   int
	area    task.AreaNode
	project task.ProjectNode
	task    task.TaskNode
	subtask task.Subtask
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
	showAll bool     // show finished tasks and projects
	view    viewKind // tree or due
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
	m.pruneFolds()
	m.rebuildRows()
	if keep != nil {
		m.selectTarget(*keep)
	}
	if m.cursor >= len(m.rows) {
		m.cursor = max(0, len(m.rows)-1)
	}
	return nil
}

// pruneFolds forgets folds on areas and projects no longer in the outline,
// so a deleted row's fold cannot land on whatever next reuses its id.
func (m *model) pruneFolds() {
	if len(m.collapsed) == 0 {
		return
	}
	present := map[target]bool{}
	var walk func(areas []task.AreaNode, projects []task.ProjectNode)
	walk = func(areas []task.AreaNode, projects []task.ProjectNode) {
		for _, a := range areas {
			present[target{rowArea, a.Area.ID}] = true
			walk(a.Areas, a.Projects)
		}
		for _, p := range projects {
			present[target{rowProject, p.Project.ID}] = true
		}
	}
	walk(m.outline.Areas, m.outline.Projects)
	for t := range m.collapsed {
		if !present[t] {
			delete(m.collapsed, t)
		}
	}
}

// rebuildRows flattens the tree for the current view. The tree view lists
// an area's areas, then its projects, each indented one step deeper, and
// skips what is inside a collapsed area or project. The due view lists open tasks
// with a due date, soonest first, with no area or project rows.
func (m *model) rebuildRows() {
	m.rows = m.rows[:0]
	areas, projects := m.outline.Areas, m.outline.Projects
	var walk func(areas []task.AreaNode, projects []task.ProjectNode, depth int)
	walk = func(areas []task.AreaNode, projects []task.ProjectNode, depth int) {
		for _, a := range areas {
			if m.view == viewTree {
				m.rows = append(m.rows, row{kind: rowArea, depth: depth, area: a})
				if m.collapsed[target{rowArea, a.Area.ID}] {
					continue
				}
			}
			walk(a.Areas, a.Projects, depth+1)
		}
		for _, p := range projects {
			if m.view == viewTree {
				m.rows = append(m.rows, row{kind: rowProject, depth: depth, project: p})
				if m.collapsed[target{rowProject, p.Project.ID}] {
					continue
				}
			}
			for _, t := range p.Tasks {
				if m.view == viewDue {
					if t.Task.Due != "" && t.Task.Open() {
						m.rows = append(m.rows, row{kind: rowTask, project: p, task: t})
					}
					continue
				}
				m.rows = append(m.rows, row{kind: rowTask, depth: depth, project: p, task: t})
				for _, s := range t.Subtasks {
					m.rows = append(m.rows, row{kind: rowSubtask, depth: depth, project: p, task: t, subtask: s})
				}
			}
		}
	}
	walk(areas, projects, 0)
	if m.view == viewDue {
		sortRowsByDue(m.rows)
	}
}

func sortRowsByDue(rows []row) {
	// Insertion sort keeps it dependency-free and stable; the list is short.
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].task.Task.Due < rows[j-1].task.Task.Due; j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

func (r row) target() target {
	switch r.kind {
	case rowArea:
		return target{rowArea, r.area.Area.ID}
	case rowTask:
		return target{rowTask, r.task.Task.ID}
	case rowSubtask:
		return target{rowSubtask, r.subtask.ID}
	}
	return target{rowProject, r.project.Project.ID}
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

// selectTarget moves the cursor to the row for t. When t is gone, for
// instance because it was just hidden, the cursor stays where it is.
func (m *model) selectTarget(t target) {
	for i, r := range m.rows {
		if r.target() == t {
			m.cursor = i
			return
		}
	}
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
	m.form = nil
	// A project's goal links may have changed; look them up afresh.
	delete(m.goalLabels, t.id)
	if err := m.reload(); err != nil {
		m.err = err
		return m, nil
	}
	m.reveal(t)
	m.status = fmt.Sprintf("saved %s", t.label())
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
			m.status = "showing finished tasks and projects"
		} else {
			m.status = "hiding finished tasks and projects"
		}
	case "v":
		if m.view == viewTree {
			m.view = viewDue
			m.status = "due: open tasks with a due date, soonest first"
		} else {
			m.view = viewTree
			m.status = "tree"
		}
		m.err = m.reload()
	case "left", "right":
		m.toggleFold()
	case "space", " ", "enter":
		m.advance()
	case "x":
		m.drop()
	case "d":
		if _, ok := m.selected(); ok {
			m.mode = modeConfirmDelete
		}
	case "n":
		return m, m.openEditor(newAreaForm(nil, m.areaHere(), m.areas, nil, 0, 0))
	case "A":
		return m, m.openEditor(newProjectForm(nil, m.areaHere(), m.areas, m.goalOptions(), 0, 0))
	case "a":
		r, ok := m.selected()
		if !ok {
			m.status = "add a project first: press A"
			return m, nil
		}
		if r.kind == rowArea {
			m.status = "select a project to add a task under; A adds one here"
			return m, nil
		}
		return m, m.openEditor(newTaskForm(nil, r.project.Project.ID, m.projectOptions(), m.now(), 0, 0))
	case "s":
		r, ok := m.selected()
		if !ok || r.kind == rowArea || r.kind == rowProject {
			m.status = "select a task to add a subtask under"
			return m, nil
		}
		return m, m.openEditor(newSubtaskForm(nil, r.task.Task, 0, 0))
	case "e":
		r, ok := m.selected()
		if !ok {
			return m, nil
		}
		switch r.kind {
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

// areaHere is the area a new area or project goes in by default: the
// selected row's area, or the top when nothing is selected.
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
// selected area or project, or shows it again when it is hidden. On a
// task or subtask it folds the project the row is in and moves the cursor
// there.
func (m *model) toggleFold() {
	r, ok := m.selected()
	if !ok {
		return
	}
	if m.view == viewDue {
		m.status = "the due list is flat; v goes back to the tree"
		return
	}
	var (
		t     target
		name  string
		held  string // what folding hides, for the status line
		empty string // why there is nothing to hide, or "" when there is
	)
	switch r.kind {
	case rowArea:
		t, name, held = r.target(), r.area.Area.Name, "what is in it"
		if len(r.area.Areas)+len(r.area.Projects) == 0 {
			empty = "the area is empty"
		}
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
// around it, so a row folded away can still be shown.
func (m *model) reveal(t target) {
	changed := false
	for _, c := range m.containers(t) {
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
		if next == task.Finished {
			m.status += m.hiddenHint()
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
	m.err = m.reload()
}

func (m *model) setProjectState(id int64, next task.State) {
	if err := m.store.MarkProject(m.ctx, id, next); err != nil {
		m.err = err
		return
	}
	m.status = fmt.Sprintf("project #%d %s", id, next)
	if next != task.Active {
		m.status += m.hiddenHint()
	}
	m.err = m.reload()
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

// drop is x: a task is dropped, a project shelved. Both are hidden unless
// finished things are shown. Pressing it again on a dropped task or a
// shelved project brings it back.
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
	case rowProject:
		st := task.Shelved
		if r.project.Project.State == task.Shelved {
			st = task.Active
		}
		if err := m.store.MarkProject(m.ctx, r.project.Project.ID, st); err != nil {
			m.err = err
			return
		}
		m.status = fmt.Sprintf("project #%d %s", r.project.Project.ID, st)
	case rowArea:
		m.status = "areas have no state; d deletes one and moves what is in it up a level"
		return
	default:
		m.status = "subtasks are ticked with space, or deleted with d"
		return
	}
	m.status += m.hiddenHint()
	m.err = m.reload()
}

// hiddenHint explains where a finished item went. The due view only ever
// lists open tasks, so there f would not bring it back.
func (m *model) hiddenHint() string {
	switch {
	case m.view == viewDue:
		return " (gone from the due list)"
	case !m.showAll:
		return " (hidden; f shows finished)"
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
		m.err = m.reload()
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
	right := paneStyle.Width(detailW).Height(bodyH).Render(clipLines(m.viewDetail(detailW-4, bodyH-2), bodyH-2))

	var b strings.Builder
	title := "tasktracker · tree"
	if m.view == viewDue {
		title = "tasktracker · due"
	}
	head := titleStyle.Render(title)
	if m.showAll {
		head += dimStyle.Render(" · showing finished")
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
		case m.view == viewDue:
			return dimStyle.Render("nothing due\n\nv goes back to the tree")
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
// red, and in the due view the project name is dimmed after the title.
func (m *model) viewRow(r row, selected bool, w int) string {
	var left, right, suffix string
	finished := false
	indent := strings.Repeat("  ", r.depth)
	switch r.kind {
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
		if m.view == viewDue {
			indent = ""
			suffix = "  " + r.project.Project.Name
		}
		left = fmt.Sprintf("%s%s %s", indent, statusGlyph(t.Status), t.Title)
		if n := len(r.task.Subtasks); n > 0 {
			right = fmt.Sprintf("%d/%d  ", r.task.Ticked(), n)
		}
		right += t.Due
		finished = !t.Open()
	case rowSubtask:
		box := "[ ]"
		if r.subtask.Done {
			box = "[x]"
		}
		left = fmt.Sprintf("%s    %s %s", indent, box, r.subtask.Title)
	}
	line := fit(left+suffix, right, w)
	switch {
	case selected:
		return selectedStyle.Render(line)
	case finished:
		return dimStyle.Render(line)
	case r.kind == rowArea:
		return areaStyle.Render(line)
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

// viewDetail renders the selected row into a w by h area.
func (m *model) viewDetail(w, h int) string {
	r, ok := m.selected()
	if !ok {
		return ""
	}
	wrap := lipgloss.NewStyle().Width(w)
	cut := func(line string) string { return ansi.Truncate(line, w, "…") }
	label := func(name, value string) string { return cut(labelStyle.Render(name) + " " + value) }
	var lines []string
	switch r.kind {
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
		lines = append(lines, label("status ", statusText(t.Status)+"   "+labelStyle.Render("id")+fmt.Sprintf(" #%d", t.ID)))
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
	if len(lines) > h {
		lines = lines[:h]
	}
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
	return "n area · A project · a task · s subtask · e edit · space next status/tick · x drop · d delete · f finished · v due · ←/→ fold/unfold · j/k move · q quit"
}
