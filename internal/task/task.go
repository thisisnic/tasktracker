// Package task holds the project, task and subtask model and its SQLite
// store.
//
// The hierarchy is project > task > subtask, with areas above projects for
// grouping: an area holds projects and other areas. A project has a name, a
// description, an end state and zero or more links to goals in goaltracker.
// A task has a title, a status, an optional due date, an optional link to
// a GitHub issue and a block of notes. A subtask is a checklist item: a
// title and a tick.
// Ticking every subtask does not finish the task; the owner marks it done
// themselves. A finished task stays in the tree until it is archived,
// which is the one way to hide it.
package task

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// State is where a project is in its life.
type State string

const (
	Active  State = "active"
	Done    State = "done"
	Shelved State = "shelved"
)

// Status is where a task is.
type Status string

const (
	Todo     Status = "todo"
	Doing    Status = "doing"
	Finished Status = "done"
	Dropped  Status = "dropped"
)

// ErrNotFound is returned when a project, task or subtask does not exist.
var ErrNotFound = errors.New("not found")

// Project is a container of tasks with an end state.
type Project struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	State       State     `json:"state"`
	AreaID      int64     `json:"area_id,omitempty"`  // the area the project is in; 0 for none
	GoalIDs     []int64   `json:"goal_ids,omitempty"` // goaltracker goal ids this project serves
	CreatedAt   time.Time `json:"created_at"`
}

// Open reports whether the project is still being worked on.
func (p Project) Open() bool { return p.State == Active }

// Task is one piece of work inside a project.
type Task struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"project_id"`
	Title     string `json:"title"`
	Status    Status `json:"status"`
	Due       string `json:"due,omitempty"`   // YYYY-MM-DD, or empty for none
	Issue     string `json:"issue,omitempty"` // a GitHub issue or pull request URL, or empty for none
	Notes     string `json:"notes,omitempty"` // free text; blank lines at either end are dropped
	// Archived is set on a finished task that has been put away. An open
	// task is never archived: marking an archived task todo or doing
	// brings it back.
	Archived  bool      `json:"archived,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Open reports whether the task is still to be done.
func (t Task) Open() bool { return t.Status == Todo || t.Status == Doing }

// DueDate parses the task's due date. ok is false when there is none.
func (t Task) DueDate() (d time.Time, ok bool) {
	if t.Due == "" {
		return time.Time{}, false
	}
	d, err := time.Parse(dueLayout, t.Due)
	return d, err == nil
}

// Overdue reports whether the task is open with a due date before today.
func (t Task) Overdue(today time.Time) bool {
	d, ok := t.DueDate()
	return ok && t.Open() && d.Before(dayOf(today))
}

// DaysUntilDue is the number of days from today to the due date: negative
// when overdue, zero when due today. ok is false when there is no due date.
func (t Task) DaysUntilDue(today time.Time) (days int, ok bool) {
	d, ok := t.DueDate()
	if !ok {
		return 0, false
	}
	return int(d.Sub(dayOf(today)).Hours() / 24), true
}

// Subtask is a checklist item under a task.
type Subtask struct {
	ID        int64     `json:"id"`
	TaskID    int64     `json:"task_id"`
	Title     string    `json:"title"`
	Done      bool      `json:"done"`
	CreatedAt time.Time `json:"created_at"`
}

const dueLayout = "2006-01-02"

// dayOf drops the time of day so date arithmetic is whole days.
func dayOf(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// ParseDue normalises a due date. Empty or "none" means no due date.
// Accepted forms: "2026-09-30", and the shorthands "today" and "tomorrow".
func ParseDue(raw string, today time.Time) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "", "none":
		return "", nil
	case "today":
		return dayOf(today).Format(dueLayout), nil
	case "tomorrow":
		return dayOf(today).AddDate(0, 0, 1).Format(dueLayout), nil
	}
	d, err := time.Parse(dueLayout, s)
	if err != nil {
		return "", fmt.Errorf("due %q: want YYYY-MM-DD, today, tomorrow or none", raw)
	}
	return d.Format(dueLayout), nil
}

// issuePath is the path of a GitHub issue or pull request page, and
// issueShort is the owner/repo#N shorthand for one.
var (
	issuePath  = regexp.MustCompile(`^/([A-Za-z0-9-]+)/([A-Za-z0-9._-]+)/(issues|pull)/([0-9]+)/?$`)
	issueShort = regexp.MustCompile(`^([A-Za-z0-9-]+)/([A-Za-z0-9._-]+)#([0-9]+)$`)
)

// ParseIssue normalises a link to a GitHub issue. Empty or "none" means no
// link. Accepted forms: an issue or pull request URL, with or without
// https://, and the shorthand owner/repo#N. Anything after the number, such
// as a comment anchor, is dropped. The result is always a full https URL,
// so every stored link can be opened as it is; the shorthand becomes an
// issues URL, which GitHub redirects to the pull request when N is one.
func ParseIssue(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" || strings.EqualFold(s, "none") {
		return "", nil
	}
	bad := fmt.Errorf("issue %q: want a GitHub issue URL, owner/repo#N or none", raw)
	if m := issueShort.FindStringSubmatch(s); m != nil {
		return issueURL(m[1], m[2], "issues", m[3], bad)
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || !strings.EqualFold(u.Host, "github.com") && !strings.EqualFold(u.Host, "www.github.com") {
		return "", bad
	}
	m := issuePath.FindStringSubmatch(u.Path)
	if m == nil {
		return "", bad
	}
	return issueURL(m[1], m[2], m[3], m[4], bad)
}

// issueURL builds the URL of an issue from its parts, or returns bad. A
// repository named . or .. would take the URL out of the repository when
// it is opened, and issue numbers start at 1.
func issueURL(owner, repo, kind, number string, bad error) (string, error) {
	n, err := strconv.Atoi(number)
	if err != nil || n <= 0 || repo == "." || repo == ".." {
		return "", bad
	}
	return fmt.Sprintf("https://github.com/%s/%s/%s/%d", owner, repo, kind, n), nil
}

// IssueRef is the task's issue link as owner/repo#N, for showing where a
// full URL would not fit, and as the https URL to open. Both are rebuilt
// from the parts ParseIssue checks, so neither can carry characters that
// a terminal would take as control codes. ok is false when there is no
// link, or it is not one ParseIssue accepts, as from a hand-edited
// database.
func (t Task) IssueRef() (ref, link string, ok bool) {
	link, err := ParseIssue(t.Issue)
	if err != nil || link == "" {
		return "", "", false
	}
	m := issuePath.FindStringSubmatch(strings.TrimPrefix(link, "https://github.com"))
	return m[1] + "/" + m[2] + "#" + m[4], link, true
}

// IssueText is the task's issue for printing in full: the https URL when
// ParseIssue accepts the stored value, and otherwise the value itself
// without its control characters, so it cannot drive the terminal.
func (t Task) IssueText() string {
	if _, link, ok := t.IssueRef(); ok {
		return link
	}
	return WithoutControls(t.Issue)
}

// WithoutControls drops control characters, C0 and C1, from s.
func WithoutControls(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// ParseState accepts active, done or shelved.
func ParseState(s string) (State, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "active", "open":
		return Active, nil
	case "done", "finished":
		return Done, nil
	case "shelved", "shelve":
		return Shelved, nil
	}
	return "", fmt.Errorf("state %q: want active, done or shelved", s)
}

// ParseStatus accepts todo, doing, done or dropped.
func ParseStatus(s string) (Status, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "todo", "to-do":
		return Todo, nil
	case "doing", "in-progress":
		return Doing, nil
	case "done", "finished":
		return Finished, nil
	case "dropped", "drop":
		return Dropped, nil
	}
	return "", fmt.Errorf("status %q: want todo, doing, done or dropped", s)
}

// Next is the status after s when stepping through a task's life: todo,
// doing, done, then back to todo. Dropped steps back to todo.
func (s Status) Next() Status {
	switch s {
	case Todo:
		return Doing
	case Doing:
		return Finished
	}
	return Todo
}

// NormaliseGoalIDs sorts goal ids, drops duplicates, and rejects ids that
// are not positive.
func NormaliseGoalIDs(ids []int64) ([]int64, error) {
	seen := map[int64]bool{}
	var out []int64
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("goal id %d: want a positive integer", id)
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// ProjectNode is a project with its tasks, for the tree view.
type ProjectNode struct {
	Project Project    `json:"project"`
	Tasks   []TaskNode `json:"tasks"`
}

// TaskNode is a task with its subtasks.
type TaskNode struct {
	Task     Task      `json:"task"`
	Subtasks []Subtask `json:"subtasks"`
}

// OpenTasks counts the project's tasks that are still todo or doing.
func (p ProjectNode) OpenTasks() int {
	n := 0
	for _, t := range p.Tasks {
		if t.Task.Open() {
			n++
		}
	}
	return n
}

// Ticked counts the subtasks that are done.
func (t TaskNode) Ticked() int {
	n := 0
	for _, s := range t.Subtasks {
		if s.Done {
			n++
		}
	}
	return n
}
