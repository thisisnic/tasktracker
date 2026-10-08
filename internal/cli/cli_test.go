package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/thisisnic/tasktracker/internal/backup"
	"github.com/thisisnic/tasktracker/internal/config"
	"github.com/thisisnic/tasktracker/internal/task"
	"github.com/thisisnic/tasktracker/internal/update"
	"github.com/thisisnic/tasktracker/internal/version"
	_ "modernc.org/sqlite"
)

type runner struct {
	t  *testing.T
	db string
	// start is given to New: the release check, when a test wants one.
	start func(context.Context) func() string
}

func newRunner(t *testing.T) *runner {
	t.Helper()
	// Keep markers and any default paths out of the developer's real
	// config and data directories.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	return &runner{t: t, db: filepath.Join(t.TempDir(), "tasktracker.db")}
}

// run executes tasktracker with args and returns stdout. It fails the test on
// error unless wantErr is true, in which case it returns the error text.
func (r *runner) run(stdin string, wantErr bool, args ...string) string {
	r.t.Helper()
	out, errOut := r.runBoth(stdin, wantErr, args...)
	if wantErr {
		return errOut
	}
	return out
}

// runBoth is run with stdout and stderr kept apart. On a wanted error the
// error text comes back in the second value.
func (r *runner) runBoth(stdin string, wantErr bool, args ...string) (string, string) {
	r.t.Helper()
	root := New(r.start)
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"--db", r.db}, args...))
	err := root.Execute()
	if wantErr {
		if err == nil {
			r.t.Fatalf("tasktracker %v succeeded, want error", args)
		}
		return out.String(), err.Error()
	}
	if err != nil {
		r.t.Fatalf("tasktracker %v: %v\n%s%s", args, err, out.String(), errOut.String())
	}
	return out.String(), errOut.String()
}

func TestProjectLifecycle(t *testing.T) {
	r := newRunner(t)
	out := r.run("", false, "project", "add", "house", "--description", "fix it up")
	if !strings.Contains(out, "added project 1: house") {
		t.Fatalf("add: %q", out)
	}
	var p task.Project
	if err := json.Unmarshal([]byte(r.run("", false, "project", "add", "work", "--json")), &p); err != nil || p.ID != 2 || p.Name != "work" || p.State != task.Active {
		t.Errorf("add --json: %+v, %v", p, err)
	}

	out = r.run("", false, "project", "show", "1")
	for _, want := range []string{"#1  house", "state:  active", "about:  fix it up"} {
		if !strings.Contains(out, want) {
			t.Errorf("show missing %q:\n%s", want, out)
		}
	}
	var detail projectDetail
	if err := json.Unmarshal([]byte(r.run("", false, "project", "show", "1", "--json")), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Name != "house" || detail.Description != "fix it up" || len(detail.Tasks) != 0 {
		t.Errorf("show --json: %+v", detail)
	}

	out = r.run("", false, "project", "list")
	if !strings.Contains(out, "ID") || !strings.Contains(out, "house") {
		t.Errorf("list:\n%s", out)
	}

	r.run("", false, "project", "edit", "1", "--name", "home")
	r.run("", false, "project", "edit", "2", "--description", "")
	if msg := r.run("", true, "project", "edit", "1"); !strings.Contains(msg, "nothing to change") {
		t.Errorf("edit with no flags: %q", msg)
	}
	var ps []task.Project
	if err := json.Unmarshal([]byte(r.run("", false, "project", "list", "--json")), &ps); err != nil || len(ps) != 2 || ps[0].Name != "home" {
		t.Errorf("after edit: %+v, %v", ps, err)
	}

	r.run("", false, "project", "mark", "2", "shelved")
	if out := r.run("", false, "project", "list"); strings.Contains(out, "work") {
		t.Errorf("shelved project listed by default:\n%s", out)
	}
	if out := r.run("", false, "project", "list", "--all"); !strings.Contains(out, "shelved") {
		t.Errorf("--all misses the shelved project:\n%s", out)
	}
	if out := r.run("", false, "project", "list", "--state", "shelved"); !strings.Contains(out, "work") || strings.Contains(out, "home") {
		t.Errorf("--state shelved:\n%s", out)
	}
	if msg := r.run("", true, "project", "mark", "2", "paused"); !strings.Contains(msg, "want active, done or shelved") {
		t.Errorf("bad state: %q", msg)
	}

	if out := r.run("n\n", false, "project", "delete", "1"); !strings.Contains(out, "kept") {
		t.Errorf("declined delete: %q", out)
	}
	if out := r.run("y\n", false, "project", "delete", "1"); !strings.Contains(out, "deleted project 1") {
		t.Errorf("confirmed delete: %q", out)
	}
	r.run("", false, "project", "delete", "2", "-y")
	if got := strings.TrimSpace(r.run("", false, "project", "list", "--all", "--json")); got != "[]" {
		t.Errorf("projects left: %s", got)
	}
	if out := r.run("", false, "project", "list"); !strings.Contains(out, "no projects") {
		t.Errorf("empty list: %q", out)
	}
}

func TestTaskLifecycle(t *testing.T) {
	r := newRunner(t)
	r.run("", false, "project", "add", "house")
	r.run("", false, "project", "add", "work")
	out := r.run("", false, "task", "add", "paint the hall", "--project", "1", "--due", "2026-10-01", "--notes", "two coats\nwhite\n")
	if !strings.Contains(out, "added task 1: paint the hall (project 1)") {
		t.Fatalf("add: %q", out)
	}
	var tk task.Task
	if err := json.Unmarshal([]byte(r.run("", false, "task", "add", "email accountant", "--project", "2", "--json")), &tk); err != nil || tk.ID != 2 || tk.Status != task.Todo || tk.Due != "" {
		t.Errorf("add --json: %+v, %v", tk, err)
	}
	if msg := r.run("", true, "task", "add", "x"); !strings.Contains(msg, "project") {
		t.Errorf("add without project: %q", msg)
	}
	if msg := r.run("", true, "task", "add", "x", "--project", "9"); !strings.Contains(msg, "project 9: not found") {
		t.Errorf("add to missing project: %q", msg)
	}
	if msg := r.run("", true, "task", "add", "x", "--project", "1", "--due", "soon"); !strings.Contains(msg, `due "soon"`) {
		t.Errorf("bad due: %q", msg)
	}

	out = r.run("", false, "task", "list")
	for _, want := range []string{"P1", "house", "paint the hall", "2026-10-01", "P2", "email accountant"} {
		if !strings.Contains(out, want) {
			t.Errorf("list missing %q:\n%s", want, out)
		}
	}
	out = r.run("", false, "task", "show", "1")
	for _, want := range []string{"#1  paint the hall", "project: #1 house", "status:  todo", "due:     2026-10-01", "notes:\n  two coats\n  white\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("show missing %q:\n%s", want, out)
		}
	}

	r.run("", false, "task", "edit", "1", "--title", "paint the hallway", "--no-due", "--project", "2")
	if msg := r.run("", true, "task", "edit", "1", "--due", "today", "--no-due"); !strings.Contains(msg, "cannot both") {
		t.Errorf("edit with both due flags: %q", msg)
	}
	if msg := r.run("", true, "task", "edit", "1", "--notes", "x", "--no-notes"); !strings.Contains(msg, "notes") || !strings.Contains(msg, "no-notes") {
		t.Errorf("edit with both notes flags: %q", msg)
	}
	if msg := r.run("", true, "task", "edit", "1"); !strings.Contains(msg, "nothing to change") {
		t.Errorf("edit with no flags: %q", msg)
	}
	var node task.TaskNode
	if err := json.Unmarshal([]byte(r.run("", false, "task", "show", "1", "--json")), &node); err != nil || node.Task.Title != "paint the hallway" || node.Task.Due != "" || node.Task.ProjectID != 2 || node.Task.Notes != "two coats\nwhite" || len(node.Subtasks) != 0 {
		t.Errorf("after edit: %+v, %v", node, err)
	}
	r.run("", false, "task", "edit", "1", "--notes", "one coat")
	var noted task.TaskNode
	if err := json.Unmarshal([]byte(r.run("", false, "task", "show", "1", "--json")), &noted); err != nil || noted.Task.Notes != "one coat" {
		t.Errorf("after --notes: %+v, %v", noted, err)
	}
	r.run("", false, "task", "edit", "1", "--no-notes")
	var cleared task.TaskNode
	if err := json.Unmarshal([]byte(r.run("", false, "task", "show", "1", "--json")), &cleared); err != nil || cleared.Task.Notes != "" {
		t.Errorf("after --no-notes: %+v, %v", cleared, err)
	}
	if out := r.run("", false, "task", "show", "1"); strings.Contains(out, "notes:") {
		t.Errorf("show prints a notes heading with none:\n%s", out)
	}

	r.run("", false, "task", "mark", "2", "done")
	for _, bad := range []string{"blocked", "doing"} {
		if msg := r.run("", true, "task", "mark", "2", bad); !strings.Contains(msg, "want todo, done or dropped") {
			t.Errorf("status %q: %q", bad, msg)
		}
	}
	// A done task stays listed until it is archived.
	out = r.run("", false, "task", "list")
	if !strings.Contains(out, "email accountant") || !strings.Contains(out, "todo") || !strings.Contains(out, "done") {
		t.Errorf("list hides the todo or done task:\n%s", out)
	}
	if out := r.run("", false, "task", "list", "--open"); strings.Contains(out, "email accountant") || !strings.Contains(out, "hallway") || !strings.Contains(out, "P2") {
		t.Errorf("--open keeps the tree without the done task:\n%s", out)
	}
	if msg := r.run("", true, "task", "list", "--open", "--status", "done"); !strings.Contains(msg, "open") || !strings.Contains(msg, "status") {
		t.Errorf("--open with --status: %q", msg)
	}
	if msg := r.run("", true, "task", "archive", "1"); !strings.Contains(msg, "task 1: task is still todo") {
		t.Errorf("archiving an open task: %q", msg)
	}
	if out := r.run("", false, "task", "archive", "2"); !strings.Contains(out, "task 2 archived") {
		t.Errorf("archive: %q", out)
	}
	out = r.run("", false, "task", "list")
	if strings.Contains(out, "email accountant") {
		t.Errorf("list shows the archived task:\n%s", out)
	}
	if out := r.run("", false, "task", "list", "--all"); !strings.Contains(out, "done (archived)") {
		t.Errorf("--all misses the archived task:\n%s", out)
	}
	if out := r.run("", false, "task", "list", "--status", "done"); strings.Contains(out, "email accountant") {
		t.Errorf("--status done lists the archived task:\n%s", out)
	}
	if out := r.run("", false, "task", "list", "--status", "done", "--all"); !strings.Contains(out, "email accountant") || strings.Contains(out, "hallway") {
		t.Errorf("--status done --all:\n%s", out)
	}
	if out := r.run("", false, "task", "show", "2"); !strings.Contains(out, "status:  done (archived)") {
		t.Errorf("show of an archived task:\n%s", out)
	}
	if out := r.run("", false, "task", "unarchive", "2"); !strings.Contains(out, "task 2 unarchived") {
		t.Errorf("unarchive: %q", out)
	}
	var back task.TaskNode
	if err := json.Unmarshal([]byte(r.run("", false, "task", "show", "2", "--json")), &back); err != nil || back.Task.Archived || back.Task.Status != task.Finished {
		t.Errorf("after unarchive: %+v, %v", back, err)
	}
	if out := r.run("", false, "task", "list", "--project", "1"); !strings.Contains(out, "no tasks match") {
		t.Errorf("--project 1 after the move:\n%s", out)
	}

	if out := r.run("n\n", false, "task", "delete", "1"); !strings.Contains(out, "kept") {
		t.Errorf("declined delete: %q", out)
	}
	if out := r.run("y\n", false, "task", "delete", "1"); !strings.Contains(out, "deleted task 1") {
		t.Errorf("confirmed delete: %q", out)
	}
	if msg := r.run("", true, "task", "show", "1"); !strings.Contains(msg, "task 1: not found") {
		t.Errorf("show deleted: %q", msg)
	}
}

func TestCopyTask(t *testing.T) {
	r := newRunner(t)
	r.run("", false, "project", "add", "house")
	r.run("", false, "project", "add", "work")
	r.run("", false, "task", "add", "paint the hall", "--project", "1", "--due", "2026-10-01", "--notes", "two coats")
	r.run("", false, "subtask", "add", "1", "buy paint")
	r.run("", false, "subtask", "add", "1", "move furniture")
	r.run("", false, "subtask", "tick", "1")
	out := r.run("", false, "task", "copy", "1")
	if !strings.Contains(out, "copied task 1 to 2: paint the hall (project 1, 2 subtasks)") {
		t.Errorf("copy: %q", out)
	}
	var node task.TaskNode
	if err := json.Unmarshal([]byte(r.run("", false, "task", "show", "2", "--json")), &node); err != nil || node.Task.Due != "2026-10-01" || node.Task.Notes != "two coats" || node.Task.Status != task.Todo || len(node.Subtasks) != 2 || node.Subtasks[0].Done {
		t.Errorf("the copy: %+v, %v", node, err)
	}
	// A fresh value each time: an omitted field would keep the old one.
	var flagged task.TaskNode
	if err := json.Unmarshal([]byte(r.run("", false, "task", "copy", "1", "--title", "paint the landing", "--no-due", "--notes", "one coat", "--project", "2", "--json")), &flagged); err != nil || flagged.Task.ID != 3 || flagged.Task.Title != "paint the landing" || flagged.Task.Due != "" || flagged.Task.Notes != "one coat" || flagged.Task.ProjectID != 2 || len(flagged.Subtasks) != 2 {
		t.Errorf("copy with flags --json: %+v, %v", flagged, err)
	}
	var unnoted task.TaskNode
	if err := json.Unmarshal([]byte(r.run("", false, "task", "copy", "1", "--no-notes", "--json")), &unnoted); err != nil || unnoted.Task.ID != 4 || unnoted.Task.Notes != "" {
		t.Errorf("copy with --no-notes: %+v, %v", unnoted, err)
	}
	if msg := r.run("", true, "task", "copy", "1", "--due", "today", "--no-due"); !strings.Contains(msg, "cannot both") {
		t.Errorf("copy with both due flags: %q", msg)
	}
	if msg := r.run("", true, "task", "copy", "1", "--notes", "x", "--no-notes"); !strings.Contains(msg, "notes") || !strings.Contains(msg, "no-notes") {
		t.Errorf("copy with both notes flags: %q", msg)
	}
	if msg := r.run("", true, "task", "copy", "9"); !strings.Contains(msg, "task 9: not found") {
		t.Errorf("copy of a missing task: %q", msg)
	}
	if msg := r.run("", true, "task", "copy", "1", "--project", "9"); !strings.Contains(msg, "project 9: not found") {
		t.Errorf("copy into a missing project: %q", msg)
	}
	if out := r.run("", false, "task", "show", "1"); !strings.Contains(out, "[x] 1  buy paint") {
		t.Errorf("original changed by the copy:\n%s", out)
	}
}

func TestTaskIssue(t *testing.T) {
	r := newRunner(t)
	r.run("", false, "project", "add", "house")
	r.run("", false, "task", "add", "fix the gate", "--project", "1", "--issue", "owner/repo#42")
	if out := r.run("", false, "task", "show", "1"); !strings.Contains(out, "issue:   https://github.com/owner/repo/issues/42") {
		t.Errorf("show:\n%s", out)
	}
	if msg := r.run("", true, "task", "add", "x", "--project", "1", "--issue", "the gate"); !strings.Contains(msg, `issue "the gate"`) {
		t.Errorf("bad issue on add: %q", msg)
	}
	r.run("", false, "task", "edit", "1", "--issue", "https://github.com/owner/repo/pull/7")
	var node task.TaskNode
	if err := json.Unmarshal([]byte(r.run("", false, "task", "show", "1", "--json")), &node); err != nil || node.Task.Issue != "https://github.com/owner/repo/pull/7" {
		t.Errorf("after edit --issue: %+v, %v", node, err)
	}
	if msg := r.run("", true, "task", "edit", "1", "--issue", "owner/repo#1", "--no-issue"); !strings.Contains(msg, "none of the others can be") {
		t.Errorf("edit with both issue flags: %q", msg)
	}
	// A copy leaves the original's issue behind unless given one.
	var plainCopy task.TaskNode
	if err := json.Unmarshal([]byte(r.run("", false, "task", "copy", "1", "--json")), &plainCopy); err != nil || plainCopy.Task.Issue != "" {
		t.Errorf("copy: %+v, %v", plainCopy, err)
	}
	var linked task.TaskNode
	if err := json.Unmarshal([]byte(r.run("", false, "task", "copy", "1", "--issue", "owner/repo#8", "--json")), &linked); err != nil || linked.Task.Issue != "https://github.com/owner/repo/issues/8" {
		t.Errorf("copy --issue: %+v, %v", linked, err)
	}
	r.run("", false, "task", "edit", "1", "--no-issue")
	if out := r.run("", false, "task", "show", "1"); strings.Contains(out, "issue:") {
		t.Errorf("show after --no-issue:\n%s", out)
	}
	var cleared task.TaskNode
	if err := json.Unmarshal([]byte(r.run("", false, "task", "show", "1", "--json")), &cleared); err != nil || cleared.Task.Issue != "" {
		t.Errorf("after --no-issue: %+v, %v", cleared, err)
	}

	// A stored issue ParseIssue did not write, as from a hand-edited
	// database, is shown without its control characters.
	db, err := sql.Open("sqlite", r.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE tasks SET issue = ? WHERE id = 1`, "the fence ticket\x1b[2J"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if out := r.run("", false, "task", "show", "1"); !strings.Contains(out, "issue:   the fence ticket[2J\n") || strings.Contains(out, "\x1b") {
		t.Errorf("show of an odd issue: %q", out)
	}
}

func TestDueListing(t *testing.T) {
	r := newRunner(t)
	r.run("", false, "project", "add", "p")
	r.run("", false, "task", "add", "later", "--project", "1", "--due", "2099-12-01")
	r.run("", false, "task", "add", "long ago", "--project", "1", "--due", "2000-01-01")
	r.run("", false, "task", "add", "whenever", "--project", "1")
	r.run("", false, "task", "add", "finished", "--project", "1", "--due", "2001-01-01")
	r.run("", false, "task", "mark", "4", "done")
	out := r.run("", false, "task", "list", "--due")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || !strings.Contains(lines[1], "long ago") || !strings.Contains(lines[1], "overdue") || !strings.Contains(lines[2], "later") || strings.Contains(lines[2], "overdue") {
		t.Errorf("--due listing:\n%s", out)
	}
	if out := r.run("", false, "task", "list", "--due", "--all"); !strings.Contains(out, "finished") {
		t.Errorf("--due --all misses the done task:\n%s", out)
	}
	if out := r.run("", false, "task", "list", "--due", "--status", "done"); !strings.Contains(out, "finished") || strings.Contains(out, "long ago") {
		t.Errorf("--due --status done:\n%s", out)
	}
	if out := r.run("", false, "task", "show", "2"); !strings.Contains(out, "days overdue") {
		t.Errorf("show of an overdue task:\n%s", out)
	}
	var tasks []task.Task
	if err := json.Unmarshal([]byte(r.run("", false, "task", "list", "--due", "--json")), &tasks); err != nil || len(tasks) != 2 || tasks[0].Title != "long ago" {
		t.Errorf("--due --json: %+v, %v", tasks, err)
	}
}

func TestSubtasks(t *testing.T) {
	r := newRunner(t)
	r.run("", false, "project", "add", "party")
	r.run("", false, "task", "add", "invites", "--project", "1")
	if out := r.run("", false, "subtask", "add", "1", "write list"); !strings.Contains(out, "added subtask 1: write list (task 1)") {
		t.Errorf("add: %q", out)
	}
	var s task.Subtask
	if err := json.Unmarshal([]byte(r.run("", false, "subtask", "add", "1", "send", "--json")), &s); err != nil || s.ID != 2 || s.Done {
		t.Errorf("add --json: %+v, %v", s, err)
	}
	if msg := r.run("", true, "subtask", "add", "9", "x"); !strings.Contains(msg, "task 9: not found") {
		t.Errorf("add to missing task: %q", msg)
	}
	r.run("", false, "subtask", "tick", "1")
	r.run("", false, "subtask", "edit", "2", "--title", "send them")
	out := r.run("", false, "task", "show", "1")
	if !strings.Contains(out, "[x] 1  write list") || !strings.Contains(out, "[ ] 2  send them") {
		t.Errorf("show after tick and edit:\n%s", out)
	}
	out = r.run("", false, "task", "list")
	if !strings.Contains(out, "S1") || !strings.Contains(out, "[x]") || !strings.Contains(out, "send them") {
		t.Errorf("tree with subtasks:\n%s", out)
	}
	// Ticking everything leaves the task open.
	r.run("", false, "subtask", "tick", "2")
	var node task.TaskNode
	if err := json.Unmarshal([]byte(r.run("", false, "task", "show", "1", "--json")), &node); err != nil || node.Task.Status != task.Todo || node.Ticked() != 2 {
		t.Errorf("task auto-closed or ticks lost: %+v, %v", node, err)
	}
	r.run("", false, "subtask", "untick", "2")
	r.run("", false, "subtask", "delete", "1")
	if msg := r.run("", true, "subtask", "delete", "1"); !strings.Contains(msg, "subtask 1: not found") {
		t.Errorf("delete twice: %q", msg)
	}
	if err := json.Unmarshal([]byte(r.run("", false, "task", "show", "1", "--json")), &node); err != nil || len(node.Subtasks) != 1 || node.Subtasks[0].Done {
		t.Errorf("after untick and delete: %+v, %v", node, err)
	}
}

func TestEmptyJSONIsArray(t *testing.T) {
	r := newRunner(t)
	for _, args := range [][]string{{"area", "list", "--json"}, {"project", "list", "--json"}, {"task", "list", "--due", "--json"}} {
		if got := strings.TrimSpace(r.run("", false, args...)); got != "[]" {
			t.Errorf("%v = %q, want []", args, got)
		}
	}
	// The tree is an object with two lists, both present when empty.
	var o task.Outline
	if err := json.Unmarshal([]byte(r.run("", false, "task", "list", "--json")), &o); err != nil || o.Areas == nil || o.Projects == nil {
		t.Errorf("task list --json: %+v, %v", o, err)
	}
}

func TestTreeJSONHasNoNulls(t *testing.T) {
	// An empty area, a project with no tasks and a task with no subtasks
	// each print [] for what they lack, as the README promises.
	r := newRunner(t)
	r.run("", false, "area", "add", "empty")
	r.run("", false, "area", "add", "home")
	r.run("", false, "project", "add", "bare", "--in", "2")
	r.run("", false, "project", "add", "house")
	r.run("", false, "task", "add", "paint", "--project", "2")
	out := r.run("", false, "task", "list", "--json")
	if strings.Contains(out, "null") {
		t.Fatalf("tree JSON has a null:\n%s", out)
	}
	var o task.Outline
	if err := json.Unmarshal([]byte(out), &o); err != nil {
		t.Fatal(err)
	}
	empty, home := o.Areas[0], o.Areas[1]
	if empty.Areas == nil || empty.Projects == nil || home.Areas == nil || home.Projects[0].Tasks == nil || o.Projects[0].Tasks[0].Subtasks == nil {
		t.Errorf("a nested list is missing: %s", out)
	}
}

func TestAreas(t *testing.T) {
	r := newRunner(t)
	out := r.run("", false, "area", "add", "home")
	if !strings.Contains(out, "added area 1: home") {
		t.Fatalf("add: %q", out)
	}
	var a task.Area
	if err := json.Unmarshal([]byte(r.run("", false, "area", "add", "garden", "--in", "1", "--json")), &a); err != nil || a.ID != 2 || a.ParentID != 1 {
		t.Errorf("add --json: %+v, %v", a, err)
	}
	r.run("", false, "area", "add", "maintenance", "--in", "1")
	if msg := r.run("", true, "area", "add", "x", "--in", "9"); !strings.Contains(msg, "area 9: not found") {
		t.Errorf("bad parent: %q", msg)
	}

	r.run("", false, "project", "add", "grant report", "--in", "2")
	r.run("", false, "project", "add", "house")
	r.run("", false, "task", "add", "draft", "--project", "1")
	if msg := r.run("", true, "project", "add", "x", "--in", "9"); !strings.Contains(msg, "area 9: not found") {
		t.Errorf("project in bad area: %q", msg)
	}

	out = r.run("", false, "area", "list")
	for _, want := range []string{"ID  AREA", "1   home", "2     garden", "3     maintenance"} {
		if !strings.Contains(out, want) {
			t.Errorf("area list missing %q:\n%s", want, out)
		}
	}
	out = r.run("", false, "project", "list")
	if !strings.Contains(out, "AREA") || !strings.Contains(out, "home / garden") {
		t.Errorf("project list:\n%s", out)
	}
	out = r.run("", false, "project", "show", "1")
	if !strings.Contains(out, "area:   home / garden") {
		t.Errorf("project show:\n%s", out)
	}
	var detail projectDetail
	if err := json.Unmarshal([]byte(r.run("", false, "project", "show", "1", "--json")), &detail); err != nil || detail.Area != "home / garden" || detail.AreaID != 2 {
		t.Errorf("show --json: %+v, %v", detail, err)
	}
	out = r.run("", false, "task", "list")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 7 || !strings.HasPrefix(lines[1], "A1") || !strings.Contains(lines[2], "  garden") ||
		!strings.Contains(lines[3], "    grant report") || !strings.Contains(lines[4], "      draft") ||
		!strings.Contains(lines[5], "  maintenance") || !strings.Contains(lines[6], "house") {
		t.Errorf("task list:\n%s", out)
	}
	var o task.Outline
	if err := json.Unmarshal([]byte(r.run("", false, "task", "list", "--json")), &o); err != nil || len(o.Areas) != 1 || len(o.Areas[0].Areas) != 2 || len(o.Projects) != 1 {
		t.Errorf("task list --json: %+v, %v", o, err)
	}

	// Moves: the project out to the top, then into maintenance; garden out
	// to the top and back in; refusals for an area put inside itself.
	r.run("", false, "project", "edit", "1", "--top")
	r.run("", false, "project", "edit", "1", "--in", "3")
	if msg := r.run("", true, "project", "edit", "1", "--in", "3", "--top"); !strings.Contains(msg, "cannot both") {
		t.Errorf("edit with both area flags: %q", msg)
	}
	var p task.Project
	if err := json.Unmarshal([]byte(r.run("", false, "project", "add", "probe", "--json")), &p); err != nil || p.AreaID != 0 {
		t.Errorf("probe: %+v, %v", p, err)
	}
	r.run("", false, "area", "edit", "2", "--top", "--name", "GARDEN")
	r.run("", false, "area", "edit", "2", "--in", "1")
	if msg := r.run("", true, "area", "edit", "1", "--in", "2"); !strings.Contains(msg, "inside itself") {
		t.Errorf("cycle: %q", msg)
	}
	if msg := r.run("", true, "area", "edit", "2"); !strings.Contains(msg, "nothing to change") {
		t.Errorf("edit with no flags: %q", msg)
	}
	if msg := r.run("", true, "area", "edit", "2", "--in", "1", "--top"); !strings.Contains(msg, "cannot both") {
		t.Errorf("edit with both flags: %q", msg)
	}
	var as []task.Area
	if err := json.Unmarshal([]byte(r.run("", false, "area", "list", "--json")), &as); err != nil || len(as) != 3 || as[1].Name != "GARDEN" || as[1].ParentID != 1 {
		t.Errorf("areas: %+v, %v", as, err)
	}

	// Deleting maintenance moves the report up into home; declining keeps it.
	if out := r.run("n\n", false, "area", "delete", "3"); !strings.Contains(out, "kept") {
		t.Errorf("declined delete: %q", out)
	}
	if out := r.run("", false, "area", "delete", "3", "--yes"); !strings.Contains(out, "deleted area 3") {
		t.Errorf("delete: %q", out)
	}
	if err := json.Unmarshal([]byte(r.run("", false, "project", "show", "1", "--json")), &detail); err != nil || detail.AreaID != 1 || detail.Area != "home" {
		t.Errorf("after delete: %+v, %v", detail, err)
	}
	if msg := r.run("", true, "area", "delete", "3", "--yes"); !strings.Contains(msg, "area 3: not found") {
		t.Errorf("delete again: %q", msg)
	}
}

func TestBadIDs(t *testing.T) {
	r := newRunner(t)
	for _, args := range [][]string{{"project", "show", "x"}, {"task", "show", "0"}, {"subtask", "tick", "1.5"}} {
		if msg := r.run("", true, args...); !strings.Contains(msg, "want a positive integer") {
			t.Errorf("%v: %q", args, msg)
		}
	}
	if msg := r.run("", true, "project", "show", "5"); !strings.Contains(msg, "project 5: not found") {
		t.Errorf("missing project: %q", msg)
	}
}

// newKey runs key new with the private key at path and returns the public
// key it printed.
func newKey(t *testing.T, r *runner, path string) (recipient string) {
	t.Helper()
	out := r.run("", false, "key", "new", "--out", path)
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "public key: ") {
			recipient = strings.TrimPrefix(line, "public key: ")
		}
	}
	if !strings.HasPrefix(recipient, "age1") {
		t.Fatalf("no public key in output:\n%s", out)
	}
	return recipient
}

// quit runs afterQuit as the TUI command would on the way out, and
// returns what it printed.
func quit(store *task.Store, db string, cfg config.Config, cfgErr error, changed bool) (string, error) {
	cmd := New(nil)
	cmd.SetContext(context.Background())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	err := afterQuit(cmd, store, db, cfg, cfgErr, changed)
	return buf.String(), err
}

func TestKeyBackupRestore(t *testing.T) {
	r := newRunner(t)
	root := t.TempDir()
	keyFile := filepath.Join(root, "key.txt")
	cfgPath := filepath.Join(root, "config.toml")
	dir := filepath.Join(root, "data-repo")

	recipient := newKey(t, r, keyFile)
	r.run("", true, "key", "new", "--out", keyFile) // refuses to overwrite

	// Without config, backup explains what to do.
	if msg := r.run("", true, "--config", cfgPath, "backup"); !strings.Contains(msg, "tasktracker key new") {
		t.Errorf("unconfigured backup error: %q", msg)
	}

	cfg := "[backup]\ndir = \"" + dir + "\"\nrecipient = \"" + recipient + "\"\nidentity_file = \"" + keyFile + "\"\non_quit = true\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	r.run("", false, "project", "add", "keep this")
	out := r.run("", false, "--config", cfgPath, "backup")
	if !strings.Contains(out, "backup: wrote ") {
		t.Fatalf("backup output: %q", out)
	}
	snapshot := strings.TrimSpace(strings.TrimPrefix(out, "backup: wrote "))
	if snapshot != filepath.Join(dir, "tasktracker.db.age") {
		t.Errorf("backup went to %q", snapshot)
	}
	if out := r.run("", false, "--config", cfgPath, "backup"); !strings.Contains(out, "no changes") {
		t.Errorf("second backup: %q", out)
	}

	r.run("", false, "project", "delete", "1", "-y")
	if got := strings.TrimSpace(r.run("", false, "project", "list", "--json")); got != "[]" {
		t.Fatal("project not deleted")
	}

	if out := r.run("n\n", false, "--config", cfgPath, "restore", snapshot); !strings.Contains(out, "kept") {
		t.Errorf("declined restore: %q", out)
	}
	if out := r.run("Yes\n", false, "--config", cfgPath, "restore", snapshot); !strings.Contains(out, "restored") {
		t.Errorf("restore should take Yes like delete does: %q", out)
	}
	// No FILE argument: restore from the configured folder.
	out = r.run("", false, "--config", cfgPath, "restore", "-y")
	if !strings.Contains(out, "restored") || !strings.Contains(out, snapshot) || !strings.Contains(out, ".bak") {
		t.Errorf("restore output: %q", out)
	}
	if out := r.run("", false, "project", "list"); !strings.Contains(out, "keep this") {
		t.Errorf("project not back after restore:\n%s", out)
	}
	// Restore without any key configured or given fails clearly.
	if msg := r.run("", true, "--config", filepath.Join(root, "none.toml"), "restore", snapshot, "-y"); !strings.Contains(msg, "no private key") {
		t.Errorf("restore without key: %q", msg)
	}
	// A key but no folder and no FILE also fails clearly.
	if msg := r.run("", true, "--config", filepath.Join(root, "none.toml"), "restore", "--identity", keyFile, "-y"); !strings.Contains(msg, "no backup file") {
		t.Errorf("restore without file: %q", msg)
	}
}

func TestAfterQuit(t *testing.T) {
	r := newRunner(t)
	root := t.TempDir()
	keyFile := filepath.Join(root, "key.txt")
	recipient := newKey(t, r, keyFile)
	store, err := task.Open(r.db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.AddProject(context.Background(), task.NewProject{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	run := func(cfg config.Config, cfgErr error, changed bool) (string, error) {
		return quit(store, r.db, cfg, cfgErr, changed)
	}
	// A config that could not be read is the error, and nothing is written.
	if _, err := run(config.Config{}, errors.New("bad toml"), true); err == nil || !strings.Contains(err.Error(), "no backup on quit: bad toml") {
		t.Errorf("broken config: %v", err)
	}
	// on_quit without a folder or key: nothing to do, no error, whether
	// or not anything changed.
	if out, err := run(config.Config{Backup: config.Backup{OnQuit: true}}, nil, true); err != nil || out != "" {
		t.Errorf("unconfigured on_quit: %q, %v", out, err)
	}
	if out, err := run(config.Config{Backup: config.Backup{OnQuit: true}}, nil, false); err != nil || out != "" {
		t.Errorf("unconfigured on_quit, unchanged: %q, %v", out, err)
	}
	// on_quit off: nothing written even though a backup is configured.
	dir := filepath.Join(root, "data-repo")
	b := config.Backup{Dir: dir, Recipient: recipient, IdentityFile: keyFile}
	if out, err := run(config.Config{Backup: b}, nil, true); err != nil || out != "" {
		t.Errorf("on_quit off: %q, %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tasktracker.db.age")); !os.IsNotExist(err) {
		t.Error("backup written with on_quit off")
	}
	// on_quit with a configured backup, but a session that changed
	// nothing: says so, and nothing is written.
	b.OnQuit = true
	if out, err := run(config.Config{Backup: b}, nil, false); err != nil || out != "backup: nothing changed this session\n" {
		t.Errorf("on_quit, unchanged: %q, %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tasktracker.db.age")); !os.IsNotExist(err) {
		t.Error("backup written after a session that changed nothing")
	}
	// on_quit after a session that changed something writes one.
	if out, err := run(config.Config{Backup: b}, nil, true); err != nil || !strings.Contains(out, "backup: wrote ") {
		t.Errorf("on_quit: %q, %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tasktracker.db.age")); err != nil {
		t.Error("no backup written on quit")
	}
}

// TestAfterQuitRetriesPush checks that with git on, a quit after a session
// that changed nothing still pushes a commit an earlier run could not.
func TestAfterQuitRetriesPush(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, v := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(v, "t")
	}
	for _, v := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(v, "t@t")
	}
	git := func(cwd string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = cwd
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	r := newRunner(t)
	root := t.TempDir()
	keyFile := filepath.Join(root, "key.txt")
	recipient := newKey(t, r, keyFile)
	// A bare "remote" and a clone of it as the data repo.
	remote := filepath.Join(root, "remote.git")
	dir := filepath.Join(root, "data-repo")
	git(root, "init", "-q", "--bare", "-b", "main", remote)
	git(root, "clone", "-q", remote, dir)
	git(dir, "commit", "-q", "--allow-empty", "-m", "init")
	git(dir, "push", "-q", "-u", "origin", "main")

	store, err := task.Open(r.db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := config.Config{Backup: config.Backup{Dir: dir, Recipient: recipient, IdentityFile: keyFile, OnQuit: true, Git: true}}
	run := func(changed bool) (string, error) {
		return quit(store, r.db, cfg, nil, changed)
	}
	// Before any backup, a look-only quit has nothing to push and does
	// not touch the repo.
	if out, err := run(false); err != nil || out != "backup: nothing changed this session\n" {
		t.Fatalf("look-only quit before any backup: %q, %v", out, err)
	}
	if _, err := store.AddProject(context.Background(), task.NewProject{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	if out, err := run(true); err != nil || !strings.Contains(out, "backup: pushed") {
		t.Fatalf("first backup: %q, %v", out, err)
	}
	// With everything pushed, a look-only quit says only that nothing
	// changed: there is nothing to push, so no claim of a push.
	if out, err := run(false); err != nil || out != "backup: nothing changed this session\n" {
		t.Fatalf("look-only quit with nothing waiting: %q, %v", out, err)
	}
	// The remote goes away, so the next backup's push fails and its
	// commit waits locally.
	hidden := remote + ".away"
	if err := os.Rename(remote, hidden); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddProject(context.Background(), task.NewProject{Name: "y"}); err != nil {
		t.Fatal(err)
	}
	if out, err := run(true); err != nil || !strings.Contains(out, "committed locally but not pushed") {
		t.Fatalf("backup with the remote away: %q, %v", out, err)
	}
	if err := os.Rename(hidden, remote); err != nil {
		t.Fatal(err)
	}
	// A quit that changed nothing still carries the waiting commit.
	out, err := run(false)
	if err != nil || !strings.Contains(out, "nothing changed this session") || !strings.Contains(out, "backup: pushed") {
		t.Fatalf("look-only quit with a commit waiting: %q, %v", out, err)
	}
	if log := git(remote, "log", "--format=%s", "main"); strings.Count(log, "tasktracker backup") != 2 {
		t.Errorf("remote log after the retry:\n%s", log)
	}
}

func TestVersion(t *testing.T) {
	r := newRunner(t)
	if out := r.run("", false, "version"); !strings.HasPrefix(out, "tasktracker ") || strings.TrimSpace(out) == "tasktracker" {
		t.Errorf("version output: %q", out)
	}
	if out := r.run("", false, "--version"); !strings.HasPrefix(out, "tasktracker ") {
		t.Errorf("--version output: %q", out)
	}
}

func TestDefaultPathOwnsItsDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix file modes")
	}
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TASKTRACKER_DB", "")
	dir := filepath.Join(data, "tasktracker")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	root := New(nil)
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"project", "list"}) // default --db
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("default data dir left at %o", info.Mode().Perm())
	}
}

func TestEnvPathDoesNotOwnItsDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix file modes")
	}
	docs := filepath.Join(t.TempDir(), "Documents")
	if err := os.Mkdir(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TASKTRACKER_DB", filepath.Join(docs, "tasks.db"))
	root := New(nil)
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"project", "list"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(docs)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("directory chosen via TASKTRACKER_DB changed to %o", info.Mode().Perm())
	}
}

func fakeLatest(t *testing.T, tag string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q,"assets":[]}`, tag)
	}))
	t.Cleanup(srv.Close)
	old := update.APIBase
	update.APIBase = srv.URL
	t.Cleanup(func() { update.APIBase = old })
}

func TestUpdateMessages(t *testing.T) {
	r := newRunner(t)
	fakeLatest(t, "v0.2.0")
	old := version.Version
	t.Cleanup(func() { version.Version = old })
	cases := []struct {
		current string
		want    string
	}{
		{"0.1.0", "update available: 0.1.0 -> 0.2.0"},
		{"0.2.0", "already the latest release, 0.2.0"},
		{"0.3.0", "ahead of the latest release 0.2.0"},
		{"dev", "not a release version"},
	}
	for _, c := range cases {
		version.Version = c.current
		out := r.run("", false, "update", "--check")
		if !strings.Contains(out, c.want) {
			t.Errorf("%q --check: %q, want %q", c.current, out, c.want)
		}
	}
	version.Version = "dev"
	if msg := r.run("", true, "update"); !strings.Contains(msg, "--force") {
		t.Errorf("update on a dev build: %q", msg)
	}
	version.Version = "0.2.0"
	if out := r.run("", false, "update"); !strings.Contains(out, "already the latest") {
		t.Errorf("update when current: %q", out)
	}
}

func TestUpdateNotice(t *testing.T) {
	r := newRunner(t)
	old := version.Version
	t.Cleanup(func() { version.Version = old })
	version.Version = "v0.3.0"
	started := 0
	r.start = func(context.Context) func() string {
		started++
		return func() string { return "0.4.0" }
	}

	const notice = "tasktracker 0.4.0 is out; this is 0.3.0. Run tasktracker update to install it.\n"
	if out, errOut := r.runBoth("", false, "version"); out != "tasktracker v0.3.0\n" || errOut != notice {
		t.Errorf("version: stdout %q stderr %q", out, errOut)
	}
	// On stderr, so --json on stdout stays clean.
	out, errOut := r.runBoth("", false, "project", "list", "--json")
	if strings.TrimSpace(out) != "[]" || errOut != notice {
		t.Errorf("project list --json: stdout %q stderr %q", out, errOut)
	}
	if started != 2 {
		t.Fatalf("check started %d times for two commands", started)
	}
	// A command that failed gets no notice: its error is what wants reading.
	if _, errOut := r.runBoth("", true, "task", "show", "99"); strings.Contains(errOut, "is out") {
		t.Errorf("notice after a failed command: %q", errOut)
	}
	// Nothing newer, nothing said.
	r.start = func(context.Context) func() string { return func() string { return "" } }
	if _, errOut := r.runBoth("", false, "version"); errOut != "" {
		t.Errorf("notice with nothing newer: %q", errOut)
	}
	// The update command asks GitHub itself, and shell completion is read
	// by the shell: neither starts a check, and neither waits on one.
	r.start = func(context.Context) func() string {
		t.Error("check started")
		return func() string { return "0.4.0" }
	}
	fakeLatest(t, "v0.4.0")
	if out, errOut := r.runBoth("", false, "update", "--check"); !strings.Contains(out, "update available: 0.3.0 -> 0.4.0") || errOut != "" {
		t.Errorf("update --check: stdout %q stderr %q", out, errOut)
	}
	// cobra's own completion writes its directive to stderr; the notice
	// must not join it.
	for _, args := range [][]string{{cobra.ShellCompRequestCmd, "task", ""}, {cobra.ShellCompNoDescRequestCmd, "task", ""}, {"completion", "bash"}} {
		if _, errOut := r.runBoth("", false, args...); strings.Contains(errOut, "is out") {
			t.Errorf("%v: stderr %q", args, errOut)
		}
	}
}

// freePort is a loopback port nothing is listening on at the moment.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// serve runs the bare command in the background with --no-open and
// returns its stdout, stderr, and a channel that carries its result
// once it ends; cancelling ctx ends it.
func (r *runner) serve(ctx context.Context, args ...string) (out, errOut *bytes.Buffer, done <-chan error) {
	r.t.Helper()
	root := New(r.start)
	out, errOut = &bytes.Buffer{}, &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs(append([]string{"--db", r.db, "--no-open"}, args...))
	errc := make(chan error, 1)
	go func() { errc <- root.ExecuteContext(ctx) }()
	return out, errOut, errc
}

// waitFor polls url until it answers, failing after a while.
func waitFor(t *testing.T, url string) *http.Response {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(url)
		if err == nil {
			return resp
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %v", url, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestServe(t *testing.T) {
	r := newRunner(t)
	port := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, errOut, done := r.serve(ctx, "--port", strconv.Itoa(port))
	resp := waitFor(t, fmt.Sprintf("http://127.0.0.1:%d/api/outline", port))
	var o struct {
		Outline task.Outline `json:"outline"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&o); err != nil || o.Outline.Projects == nil {
		t.Errorf("outline: %+v, %v", o, err)
	}
	resp.Body.Close()
	// Something the CLI wrote is what the API serves.
	r.run("", false, "project", "add", "house")
	resp = waitFor(t, fmt.Sprintf("http://127.0.0.1:%d/api/projects", port))
	var ps []task.Project
	if err := json.NewDecoder(resp.Body).Decode(&ps); err != nil || len(ps) != 1 || ps[0].Name != "house" {
		t.Errorf("projects: %+v, %v", ps, err)
	}
	resp.Body.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop")
	}
	if want := fmt.Sprintf("serving http://127.0.0.1:%d/ (database %s)", port, r.db); !strings.Contains(out.String(), want) {
		t.Errorf("stdout %q, want %q", out.String(), want)
	}
	if errOut.String() != "" {
		t.Errorf("stderr %q", errOut.String())
	}
	// Without [backup] there is no backup loop, and nothing said about one.
	if strings.Contains(out.String(), "backup") {
		t.Errorf("backup output without a backup configured:\n%s", out.String())
	}
}

func TestServePortFromConfig(t *testing.T) {
	r := newRunner(t)
	port := freePort(t)
	cfg := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf("port = %d\n", port)), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, _, done := r.serve(ctx, "--config", cfg)
	waitFor(t, fmt.Sprintf("http://127.0.0.1:%d/api/version", port)).Body.Close()
	cancel()
	if err := <-done; err != nil {
		t.Errorf("serve: %v", err)
	}
	if want := fmt.Sprintf("serving http://127.0.0.1:%d/", port); !strings.Contains(out.String(), want) {
		t.Errorf("stdout %q, want %q", out.String(), want)
	}

	// A broken config is reported and the default port stands, as the
	// defaults do for the TUI; so does a config whose port is out of
	// range, rather than port 0 binding a random one the printed URL
	// does not name. The default port is held here first, so that the
	// run ends at once with the error for a taken port, which names the
	// port it tried; held by this test or by a tasktracker already
	// serving on this machine, the error is the same.
	if ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", config.DefaultPort)); err == nil {
		defer ln.Close()
	}
	for _, bad := range []string{"[backup\n", "port = 0\n", "port = 70000\n"} {
		if err := os.WriteFile(cfg, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		_, errOut, done := r.serve(ctx, "--config", cfg)
		if err := <-done; err == nil || !strings.Contains(err.Error(), fmt.Sprintf("port %d is already in use", config.DefaultPort)) {
			t.Errorf("%q: serve: %v", bad, err)
		}
		if !strings.Contains(errOut.String(), "note: config: ") || !strings.Contains(errOut.String(), "using the default port") {
			t.Errorf("%q: stderr %q", bad, errOut.String())
		}
	}

	// With --port the config's port is not used, so the note does not
	// name the default port: a broken file is noted as ignored, and a
	// bad port in it not at all.
	port = freePort(t)
	for bad, want := range map[string]string{
		"[backup\n":      "note: config: " + cfg + ": toml: ",
		"port = 70000\n": "",
	} {
		if err := os.WriteFile(cfg, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		// The earlier runs ended on their cancelled context; this one
		// has to come up, so it gets its own.
		ctx, cancel := context.WithCancel(context.Background())
		out, errOut, done := r.serve(ctx, "--config", cfg, "--port", strconv.Itoa(port))
		waitFor(t, fmt.Sprintf("http://127.0.0.1:%d/api/version", port)).Body.Close()
		cancel()
		if err := <-done; err != nil {
			t.Errorf("%q with --port: %v", bad, err)
		}
		if !strings.Contains(out.String(), fmt.Sprintf("serving http://127.0.0.1:%d/", port)) {
			t.Errorf("%q with --port: stdout %q", bad, out.String())
		}
		got := errOut.String()
		if strings.Contains(got, "default port") || !strings.HasPrefix(got, want) || (want != "" && !strings.HasSuffix(got, "; ignoring it\n")) {
			t.Errorf("%q with --port: stderr %q, want prefix %q", bad, got, want)
		}
	}
}

func TestServeBadPort(t *testing.T) {
	r := newRunner(t)
	for _, p := range []string{"0", "70000", "-1"} {
		if msg := r.run("", true, "--no-open", "--port", p); !strings.Contains(msg, "--port must be between 1 and 65535") {
			t.Errorf("--port %s: %q", p, msg)
		}
	}
	// A port already taken is refused rather than bound at random.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if msg := r.run("", true, "--no-open", "--port", strconv.Itoa(port)); !strings.Contains(msg, fmt.Sprintf("port %d is already in use", port)) {
		t.Errorf("taken port: %q", msg)
	}
}

// syncBuffer is a bytes.Buffer a test can read while a serving command
// still writes to it from another goroutine.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// waitUntil polls until cond holds or the test's patience runs out.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// serveWithBackups starts the bare command with [backup] pointing at dir
// and short backup intervals, and returns its combined output. The
// server is stopped and waited for in a cleanup, so a failure part way
// still shuts it down before the interval variables are restored
// (cleanups run last-in first-out). Stopping must return nil, and a run
// cut short by the shutdown is not a failure.
func serveWithBackups(t *testing.T, r *runner, dir string) *syncBuffer {
	t.Helper()
	recipient := newKey(t, r, filepath.Join(t.TempDir(), "key.txt"))
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[backup]\ndir = \""+dir+"\"\nrecipient = \""+recipient+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldEvery, oldPoll := autoBackupEvery, autoBackupPoll
	autoBackupEvery, autoBackupPoll = 20*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { autoBackupEvery, autoBackupPoll = oldEvery, oldPoll })

	ctx, cancel := context.WithCancel(context.Background())
	out := &syncBuffer{}
	root := New(r.start)
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"--db", r.db, "--config", cfgPath, "--no-open", "--port", strconv.Itoa(freePort(t))})
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("serve: %v\n%s", err, out.String())
			}
			if s := out.String(); strings.Contains(s, "context canceled") {
				t.Errorf("shutdown reported as a backup failure:\n%s", s)
			}
		case <-time.After(10 * time.Second):
			t.Error("serve did not stop")
		}
	})
	return out
}

func TestServeBacksUpWhenChanged(t *testing.T) {
	r := newRunner(t)
	r.run("", false, "project", "add", "house")
	dir := filepath.Join(t.TempDir(), "backups")
	out := serveWithBackups(t, r, dir)

	// The first backup is written as serving starts, without being asked.
	file := filepath.Join(dir, backup.FileName)
	waitUntil(t, "the first backup", func() bool { return strings.Contains(out.String(), "backup: wrote ") })
	first, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	// Runs with nothing changed write nothing.
	waitUntil(t, "a skipped run", func() bool { return strings.Contains(out.String(), "backup: no changes since the last backup") })
	if cur, _ := os.ReadFile(file); !bytes.Equal(cur, first) {
		t.Fatal("unchanged database was backed up again")
	}
	// A change made elsewhere is picked up by the next run.
	r.run("", false, "project", "add", "garden")
	waitUntil(t, "a backup of the change", func() bool { return strings.Count(out.String(), "backup: wrote ") >= 2 })
	if cur, _ := os.ReadFile(file); bytes.Equal(cur, first) {
		t.Fatal("second backup reported but the file is unchanged")
	}
}

func TestServeRetriesAFailedBackup(t *testing.T) {
	r := newRunner(t)
	r.run("", false, "project", "add", "house")
	// A regular file where the backup folder should be makes every run
	// fail at creating the folder.
	dir := filepath.Join(t.TempDir(), "backups")
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out := serveWithBackups(t, r, dir)

	// The failure is reported, serving goes on, and the next run fails
	// the same way rather than giving up.
	retrying := func(n int) func() bool {
		return func() bool { return strings.Count(out.String(), "trying again in ") >= n }
	}
	waitUntil(t, "the first failure", retrying(1))
	waitUntil(t, "a second attempt", retrying(2))
	if s := out.String(); !strings.Contains(s, "backup: ") || strings.Contains(s, "backup: wrote ") {
		t.Fatalf("expected backup errors and no backup, got\n%s", s)
	}
	// Once the folder can be made, the next run succeeds.
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "a backup after the fix", func() bool { return strings.Contains(out.String(), "backup: wrote ") })
	if _, err := os.Stat(filepath.Join(dir, backup.FileName)); err != nil {
		t.Fatal(err)
	}
}

// TestAutoBackupFollowsTheWallClock runs the loop against a clock the
// test moves, with the real 12-hour interval: a jump past the interval
// between two polls, as a laptop waking from sleep makes, is a run, a
// jump short of it is not, and a clock set backwards starts over. The
// fake clock counts its calls, one per poll, so "no run" is asserted
// only after polls that saw the moved clock.
func TestAutoBackupFollowsTheWallClock(t *testing.T) {
	r := newRunner(t)
	recipient := newKey(t, r, filepath.Join(t.TempDir(), "key.txt"))
	b := config.Backup{Dir: filepath.Join(t.TempDir(), "backups"), Recipient: recipient}
	store, err := task.Open(r.db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var mu sync.Mutex
	now := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	polls := 0
	oldNow, oldPoll := autoBackupNow, autoBackupPoll
	autoBackupNow = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		polls++
		return now
	}
	autoBackupPoll = 5 * time.Millisecond
	t.Cleanup(func() { autoBackupNow, autoBackupPoll = oldNow, oldPoll })
	// set moves the clock by d and returns the poll count at that moment,
	// so a caller can wait for polls that happened after the move.
	set := func(d time.Duration) int {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(d)
		return polls
	}
	polled := func(since int) func() bool {
		return func() bool { mu.Lock(); defer mu.Unlock(); return polls >= since+3 }
	}
	out := &syncBuffer{}
	runs := func() int { return strings.Count(out.String(), "backup: ") }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); autoBackup(ctx, out, out, store, r.db, b) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("autoBackup did not stop")
		}
	})

	waitUntil(t, "the first run", func() bool { return runs() == 1 })
	// Short of the interval, polls come and go without a run.
	waitUntil(t, "polls after a small step", polled(set(autoBackupEvery-time.Minute)))
	if n := runs(); n != 1 {
		t.Fatalf("%d runs after %s, want 1", n, autoBackupEvery-time.Minute)
	}
	// One more minute and the interval has passed: the clock jumped
	// while the ticker slept only milliseconds.
	set(time.Minute)
	waitUntil(t, "a run after the interval", func() bool { return runs() == 2 })
	// Set back an hour, the schedule starts over at once rather than
	// waiting 13 hours for the clock to catch up.
	set(-time.Hour)
	waitUntil(t, "a run after the clock went back", func() bool { return runs() == 3 })
	// And from there the interval counts again from the new time.
	waitUntil(t, "polls after the restart", polled(set(autoBackupEvery-time.Minute)))
	if n := runs(); n != 3 {
		t.Fatalf("%d runs after the restart, want 3", n)
	}
	if s := out.String(); strings.Contains(s, "trying again") {
		t.Fatalf("a run failed:\n%s", s)
	}
}

func TestTUICommandExists(t *testing.T) {
	r := newRunner(t)
	out := r.run("", false, "help")
	if !strings.Contains(out, "tui") || !strings.Contains(out, "Open the terminal UI") {
		t.Errorf("help: %q", out)
	}
	if msg := r.run("", true, "tui", "extra"); msg != `unknown command "extra" for "tasktracker tui"` {
		t.Errorf("tui with an argument: %q", msg)
	}
}
