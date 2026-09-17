package tui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/thisisnic/tasktracker/internal/goallink"
	"github.com/thisisnic/tasktracker/internal/task"
)

// huhForm wraps a huh form with the parts of editor that are the same for
// every form here.
type huhForm struct {
	form   *huh.Form
	width  int
	height int
}

func (f *huhForm) Init() tea.Cmd { return f.form.Init() }
func (f *huhForm) View() string  { return f.form.View() }
func (f *huhForm) help() string  { return "enter next · shift+tab back · esc cancel" }

// Update feeds a message to the form and reports whether it has finished.
func (f *huhForm) Update(msg tea.Msg) (done bool, submitted bool, cmd tea.Cmd) {
	m, cmd := f.form.Update(msg)
	if fm, ok := m.(*huh.Form); ok {
		f.form = fm
	}
	switch f.form.State {
	case huh.StateCompleted:
		return true, true, cmd
	case huh.StateAborted:
		return true, false, cmd
	}
	return false, false, cmd
}

// retry puts a completed form back to work with its field values, which
// live in the owning form's variables, untouched.
func (f *huhForm) retry() tea.Cmd {
	f.form.State = huh.StateNormal
	return f.form.Init()
}

// resize fits the form to the space the TUI gives it.
func (f *huhForm) resize(width, height int) {
	f.width, f.height = max(20, width), max(10, height)
	f.form = f.form.WithWidth(f.width).WithHeight(f.height)
}

// filtering reports whether the focused field is a select with its filter
// open, in which case Esc belongs to the field rather than to the form.
func (f *huhForm) filtering() bool {
	switch sel := f.form.GetFocusedField().(type) {
	case *huh.Select[int64]:
		return sel.GetFiltering()
	case *huh.Select[task.Status]:
		return sel.GetFiltering()
	case *huh.Select[task.State]:
		return sel.GetFiltering()
	case *huh.MultiSelect[int64]:
		return sel.GetFiltering()
	}
	return false
}

func required(what string) func(string) error {
	return func(s string) error {
		if strings.TrimSpace(s) == "" {
			return errors.New("say what the " + what + " is")
		}
		return nil
	}
}

// ---- area ----

// areaForm adds or edits an area: a name and the area it sits in.
type areaForm struct {
	huhForm
	editID int64 // 0 when adding
	name   string
	parent int64 // 0 for the top level
}

// areaOptions is a pick list of areas by path, with none first under the
// given label. Areas in blocked are left out: when editing an area, that
// is the area itself and everything inside it.
func areaOptions(none string, areas []task.Area, blocked map[int64]bool) []huh.Option[int64] {
	opts := []huh.Option[int64]{huh.NewOption(none, int64(0))}
	for _, a := range areas {
		if !blocked[a.ID] {
			opts = append(opts, huh.NewOption(task.AreaPath(areas, a.ID), a.ID))
		}
	}
	return opts
}

func newAreaForm(existing *task.Area, parentID int64, areas []task.Area, blocked map[int64]bool, width, height int) *areaForm {
	f := &areaForm{parent: parentID}
	if existing != nil {
		f.editID = existing.ID
		f.name = existing.Name
		f.parent = existing.ParentID
	}
	title := "New area"
	if existing != nil {
		title = fmt.Sprintf("Edit area #%d", existing.ID)
	}
	fields := []huh.Field{
		huh.NewInput().Title("Area").Description("a name to group projects under").Value(&f.name).Validate(required("area")),
	}
	if opts := areaOptions("(top level)", areas, blocked); len(opts) > 1 {
		fields = append(fields, huh.NewSelect[int64]().Title("Inside").Options(opts...).Value(&f.parent).Height(8))
	} else {
		f.parent = 0
	}
	f.form = huh.NewForm(huh.NewGroup(fields...).Title(title)).WithShowHelp(true)
	f.resize(width, height)
	return f
}

func (f *areaForm) apply(m *model) (target, error) {
	if f.editID == 0 {
		a, err := m.store.AddArea(m.ctx, task.NewArea{Name: f.name, ParentID: f.parent})
		return target{rowArea, a.ID}, err
	}
	a, err := m.store.UpdateArea(m.ctx, f.editID, task.AreaEdit{Name: &f.name, ParentID: &f.parent})
	return target{rowArea, a.ID}, err
}

// ---- project ----

// projectForm adds or edits a project. Goals come from goaltracker as a
// pick list when its database can be read, and as a typed list of ids
// otherwise. The area field is only shown once there are areas.
type projectForm struct {
	huhForm
	editID int64 // 0 when adding

	name        string
	description string
	area        int64 // 0 for none
	state       task.State
	goalPicks   []int64 // when a pick list is offered
	goalText    string  // otherwise: comma-separated ids
	pickList    bool
}

// goalOptions lists goaltracker's goals for the project form, or nil when
// they cannot be read, in which case the form takes typed ids.
func (m *model) goalOptions() []goallink.Goal {
	if m.goals == nil {
		return nil
	}
	goals, err := m.goals.All(m.ctx)
	if err != nil {
		return nil
	}
	return goals
}

func newProjectForm(existing *task.Project, areaID int64, areas []task.Area, goals []goallink.Goal, width, height int) *projectForm {
	f := &projectForm{area: areaID, state: task.Active, pickList: len(goals) > 0}
	if existing != nil {
		f.editID = existing.ID
		f.name = existing.Name
		f.description = existing.Description
		f.area = existing.AreaID
		f.state = existing.State
		f.goalPicks = append([]int64{}, existing.GoalIDs...)
		f.goalText = idsText(existing.GoalIDs)
	}
	title := "New project"
	if existing != nil {
		title = fmt.Sprintf("Edit project #%d", existing.ID)
	}
	fields := []huh.Field{
		huh.NewInput().Title("Project").Value(&f.name).Validate(required("project")),
		huh.NewText().Title("About").Lines(3).Value(&f.description),
	}
	if len(areas) > 0 {
		fields = append(fields, huh.NewSelect[int64]().Title("Area").Options(areaOptions("(none)", areas, nil)...).Value(&f.area).Height(8))
	} else {
		f.area = 0
	}
	if existing != nil {
		fields = append(fields, huh.NewSelect[task.State]().Title("State").Options(
			huh.NewOption("active", task.Active),
			huh.NewOption("done", task.Done),
			huh.NewOption("shelved", task.Shelved),
		).Value(&f.state))
	}
	if f.pickList {
		// Goals linked to ids that goaltracker no longer has would be lost
		// on save, so keep them as options too.
		known := map[int64]bool{}
		var opts []huh.Option[int64]
		for _, g := range goals {
			known[g.ID] = true
			opts = append(opts, huh.NewOption(g.Period+"  "+g.Label(), g.ID))
		}
		for _, id := range f.goalPicks {
			if !known[id] {
				opts = append(opts, huh.NewOption(fmt.Sprintf("#%d (not in goaltracker)", id), id))
			}
		}
		fields = append(fields, huh.NewMultiSelect[int64]().Title("Goals").Description("goaltracker goals this project serves; x to pick").
			Options(opts...).Value(&f.goalPicks).Height(8))
	} else {
		fields = append(fields, huh.NewInput().Title("Goals").Description("goaltracker goal ids, comma-separated; blank for none").
			Value(&f.goalText).Validate(func(s string) error { _, err := parseIDs(s); return err }))
	}
	f.form = huh.NewForm(huh.NewGroup(fields...).Title(title)).WithShowHelp(true)
	f.resize(width, height)
	return f
}

func (f *projectForm) help() string {
	return "enter next · shift+tab back · ctrl+j new line in about · x picks a goal · esc cancel"
}

func idsText(ids []int64) string {
	var parts []string
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return strings.Join(parts, ", ")
}

// parseIDs reads a comma- or space-separated list of positive integers.
func parseIDs(s string) ([]int64, error) {
	var out []int64
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		part = strings.TrimPrefix(part, "#")
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%q is not a goal id", part)
		}
		out = append(out, id)
	}
	return out, nil
}

func (f *projectForm) apply(m *model) (target, error) {
	goals := f.goalPicks
	if !f.pickList {
		var err error
		if goals, err = parseIDs(f.goalText); err != nil {
			return target{}, err
		}
	}
	if f.editID == 0 {
		p, err := m.store.AddProject(m.ctx, task.NewProject{Name: f.name, Description: f.description, AreaID: f.area, GoalIDs: goals})
		return target{rowProject, p.ID}, err
	}
	p, err := m.store.UpdateProject(m.ctx, f.editID, task.ProjectEdit{Name: &f.name, Description: &f.description, State: &f.state, AreaID: &f.area, GoalIDs: &goals})
	return target{rowProject, p.ID}, err
}

// ---- task ----

type taskForm struct {
	huhForm
	editID  int64
	title   string
	due     string
	status  task.Status
	project int64
	today   time.Time
}

// projectOptions lists projects for the task form's project field. Every
// project is offered, finished ones included, so a task can be moved
// anywhere.
func (m *model) projectOptions() []task.Project {
	projects, err := m.store.ListProjects(m.ctx, task.ProjectFilter{All: true})
	if err != nil {
		return nil
	}
	return projects
}

func newTaskForm(existing *task.Task, projectID int64, projects []task.Project, today time.Time, width, height int) *taskForm {
	f := &taskForm{project: projectID, status: task.Todo, today: today}
	if existing != nil {
		f.editID = existing.ID
		f.title = existing.Title
		f.due = existing.Due
		f.status = existing.Status
		f.project = existing.ProjectID
	}
	title := "New task"
	if existing != nil {
		title = fmt.Sprintf("Edit task #%d", existing.ID)
	}
	var opts []huh.Option[int64]
	for _, p := range projects {
		label := p.Name
		if p.State != task.Active {
			label += " (" + string(p.State) + ")"
		}
		opts = append(opts, huh.NewOption(label, p.ID))
	}
	fields := []huh.Field{
		huh.NewInput().Title("Task").Value(&f.title).Validate(required("task")),
		huh.NewInput().Title("Due").Description("YYYY-MM-DD, today, tomorrow, or blank").Value(&f.due).
			Validate(func(s string) error { _, err := task.ParseDue(s, f.today); return err }),
	}
	if existing != nil {
		fields = append(fields, huh.NewSelect[task.Status]().Title("Status").Options(
			huh.NewOption("todo", task.Todo),
			huh.NewOption("doing", task.Doing),
			huh.NewOption("done", task.Finished),
			huh.NewOption("dropped", task.Dropped),
		).Value(&f.status))
	}
	fields = append(fields, huh.NewSelect[int64]().Title("Project").Options(opts...).Value(&f.project).Height(8))
	f.form = huh.NewForm(huh.NewGroup(fields...).Title(title)).WithShowHelp(true)
	f.resize(width, height)
	return f
}

func (f *taskForm) apply(m *model) (target, error) {
	due, err := task.ParseDue(f.due, f.today)
	if err != nil {
		return target{}, err
	}
	if f.editID == 0 {
		t, err := m.store.AddTask(m.ctx, task.NewTask{ProjectID: f.project, Title: f.title, Due: due})
		return target{rowTask, t.ID}, err
	}
	t, err := m.store.UpdateTask(m.ctx, f.editID, task.TaskEdit{Title: &f.title, Due: &due, Status: &f.status, ProjectID: &f.project})
	return target{rowTask, t.ID}, err
}

// ---- subtask ----

type subtaskForm struct {
	huhForm
	editID int64
	taskID int64
	title  string
}

func newSubtaskForm(existing *task.Subtask, under task.Task, width, height int) *subtaskForm {
	f := &subtaskForm{taskID: under.ID}
	if existing != nil {
		f.editID = existing.ID
		f.title = existing.Title
	}
	title := fmt.Sprintf("New subtask under #%d %s", under.ID, under.Title)
	if existing != nil {
		title = fmt.Sprintf("Edit subtask #%d", existing.ID)
	}
	f.form = huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Subtask").Value(&f.title).Validate(required("subtask")),
	).Title(title)).WithShowHelp(true)
	f.resize(width, height)
	return f
}

func (f *subtaskForm) help() string { return "enter save · esc cancel" }

func (f *subtaskForm) apply(m *model) (target, error) {
	if f.editID == 0 {
		s, err := m.store.AddSubtask(m.ctx, f.taskID, f.title)
		return target{rowSubtask, s.ID}, err
	}
	s, err := m.store.RenameSubtask(m.ctx, f.editID, f.title)
	return target{rowSubtask, s.ID}, err
}
