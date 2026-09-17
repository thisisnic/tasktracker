package cli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thisisnic/tasktracker/internal/task"
	"github.com/thisisnic/tasktracker/internal/update"
	"github.com/thisisnic/tasktracker/internal/version"
	_ "modernc.org/sqlite"
)

type runner struct {
	t  *testing.T
	db string
}

func newRunner(t *testing.T) *runner {
	t.Helper()
	// Keep markers and any default paths out of the developer's real
	// config and data directories, and keep goaltracker lookups away from
	// their real goals.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GOALTRACKER_DB", filepath.Join(t.TempDir(), "absent.db"))
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
	root := New()
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

// goaltrackerDB writes a database shaped like goaltracker's and points
// GOALTRACKER_DB at it.
func goaltrackerDB(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "goaltracker.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE goals (id INTEGER PRIMARY KEY, statement TEXT NOT NULL, period TEXT NOT NULL);
		INSERT INTO goals VALUES (3, 'run 500 km', '2026'), (7, 'finish the garden', '2026-Q3')`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOALTRACKER_DB", path)
}

func TestProjectLifecycle(t *testing.T) {
	r := newRunner(t)
	goaltrackerDB(t)
	out := r.run("", false, "project", "add", "house", "--description", "fix it up", "--goal", "7", "--goal", "3")
	if !strings.Contains(out, "added project 1: house") {
		t.Fatalf("add: %q", out)
	}
	var p task.Project
	if err := json.Unmarshal([]byte(r.run("", false, "project", "add", "work", "--json")), &p); err != nil || p.ID != 2 || p.Name != "work" || p.State != task.Active {
		t.Errorf("add --json: %+v, %v", p, err)
	}

	out = r.run("", false, "project", "show", "1")
	for _, want := range []string{"#1  house", "state:  active", "about:  fix it up", "#3 run 500 km", "#7 finish the garden"} {
		if !strings.Contains(out, want) {
			t.Errorf("show missing %q:\n%s", want, out)
		}
	}
	var detail projectDetail
	if err := json.Unmarshal([]byte(r.run("", false, "project", "show", "1", "--json")), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Goals) != 2 || detail.Goals[0].ID != 3 || detail.Goals[0].Statement != "run 500 km" || len(detail.Tasks) != 0 {
		t.Errorf("show --json: %+v", detail)
	}

	out = r.run("", false, "project", "list")
	if !strings.Contains(out, "ID") || !strings.Contains(out, "house") || !strings.Contains(out, "#3 #7") {
		t.Errorf("list:\n%s", out)
	}

	r.run("", false, "project", "edit", "1", "--name", "home", "--goal", "3")
	r.run("", false, "project", "edit", "2", "--no-goals", "--description", "")
	if msg := r.run("", true, "project", "edit", "1"); !strings.Contains(msg, "nothing to change") {
		t.Errorf("edit with no flags: %q", msg)
	}
	if msg := r.run("", true, "project", "edit", "1", "--goal", "1", "--no-goals"); !strings.Contains(msg, "cannot both") {
		t.Errorf("edit with both goal flags: %q", msg)
	}
	var ps []task.Project
	if err := json.Unmarshal([]byte(r.run("", false, "project", "list", "--json")), &ps); err != nil || len(ps) != 2 || ps[0].Name != "home" || len(ps[0].GoalIDs) != 1 || ps[0].GoalIDs[0] != 3 {
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

func TestProjectShowWithoutGoaltracker(t *testing.T) {
	r := newRunner(t) // GOALTRACKER_DB points at a file that does not exist
	r.run("", false, "project", "add", "house", "--goal", "3")
	out, errOut := r.runBoth("", false, "project", "show", "1")
	if !strings.Contains(out, "  #3\n") {
		t.Errorf("bare goal id not shown:\n%s", out)
	}
	if !strings.Contains(errOut, "goal statements not shown") {
		t.Errorf("missing goaltracker not explained on stderr: %q", errOut)
	}
	if _, err := os.Stat(os.Getenv("GOALTRACKER_DB")); !os.IsNotExist(err) {
		t.Error("show created a goaltracker database")
	}
}

func TestTaskLifecycle(t *testing.T) {
	r := newRunner(t)
	r.run("", false, "project", "add", "house")
	r.run("", false, "project", "add", "work")
	out := r.run("", false, "task", "add", "paint the hall", "--project", "1", "--due", "2026-10-01")
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
	for _, want := range []string{"#1  paint the hall", "project: #1 house", "status:  todo", "due:     2026-10-01"} {
		if !strings.Contains(out, want) {
			t.Errorf("show missing %q:\n%s", want, out)
		}
	}

	r.run("", false, "task", "edit", "1", "--title", "paint the hallway", "--no-due", "--project", "2")
	if msg := r.run("", true, "task", "edit", "1", "--due", "today", "--no-due"); !strings.Contains(msg, "cannot both") {
		t.Errorf("edit with both due flags: %q", msg)
	}
	if msg := r.run("", true, "task", "edit", "1"); !strings.Contains(msg, "nothing to change") {
		t.Errorf("edit with no flags: %q", msg)
	}
	var node task.TaskNode
	if err := json.Unmarshal([]byte(r.run("", false, "task", "show", "1", "--json")), &node); err != nil || node.Task.Title != "paint the hallway" || node.Task.Due != "" || node.Task.ProjectID != 2 || len(node.Subtasks) != 0 {
		t.Errorf("after edit: %+v, %v", node, err)
	}

	r.run("", false, "task", "mark", "1", "doing")
	r.run("", false, "task", "mark", "2", "done")
	if msg := r.run("", true, "task", "mark", "2", "blocked"); !strings.Contains(msg, "want todo, doing, done or dropped") {
		t.Errorf("bad status: %q", msg)
	}
	out = r.run("", false, "task", "list")
	if strings.Contains(out, "email accountant") || !strings.Contains(out, "doing") {
		t.Errorf("list hides doing or shows done:\n%s", out)
	}
	if out := r.run("", false, "task", "list", "--all"); !strings.Contains(out, "email accountant") {
		t.Errorf("--all misses the done task:\n%s", out)
	}
	if out := r.run("", false, "task", "list", "--status", "done"); !strings.Contains(out, "email accountant") || strings.Contains(out, "hallway") {
		t.Errorf("--status done:\n%s", out)
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

func TestDueListing(t *testing.T) {
	r := newRunner(t)
	r.run("", false, "project", "add", "p")
	r.run("", false, "task", "add", "later", "--project", "1", "--due", "2099-12-01")
	r.run("", false, "task", "add", "long ago", "--project", "1", "--due", "2000-01-01")
	r.run("", false, "task", "add", "whenever", "--project", "1")
	out := r.run("", false, "task", "list", "--due")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || !strings.Contains(lines[1], "long ago") || !strings.Contains(lines[1], "overdue") || !strings.Contains(lines[2], "later") || strings.Contains(lines[2], "overdue") {
		t.Errorf("--due listing:\n%s", out)
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
	for _, args := range [][]string{{"project", "list", "--json"}, {"task", "list", "--json"}, {"task", "list", "--due", "--json"}} {
		if got := strings.TrimSpace(r.run("", false, args...)); got != "[]" {
			t.Errorf("%v = %q, want []", args, got)
		}
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

func TestBareCommandShowsTree(t *testing.T) {
	r := newRunner(t)
	r.run("", false, "project", "add", "house")
	if out := r.run("", false); !strings.Contains(out, "house") {
		t.Errorf("bare command:\n%s", out)
	}
}

func TestKeyBackupRestore(t *testing.T) {
	r := newRunner(t)
	root := t.TempDir()
	keyFile := filepath.Join(root, "key.txt")
	cfgPath := filepath.Join(root, "config.toml")
	dir := filepath.Join(root, "data-repo")

	out := r.run("", false, "key", "new", "--out", keyFile)
	var recipient string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "public key: ") {
			recipient = strings.TrimPrefix(line, "public key: ")
		}
	}
	if !strings.HasPrefix(recipient, "age1") {
		t.Fatalf("no public key in output:\n%s", out)
	}
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
	out = r.run("", false, "--config", cfgPath, "backup")
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

func TestGoaltrackerPathFromConfig(t *testing.T) {
	r := newRunner(t)
	goaltrackerDB(t)
	// The config names the database explicitly; the env var points elsewhere.
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[goaltracker]\ndb = \""+os.Getenv("GOALTRACKER_DB")+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOALTRACKER_DB", filepath.Join(t.TempDir(), "absent.db"))
	r.run("", false, "project", "add", "house", "--goal", "3")
	if out := r.run("", false, "--config", cfgPath, "project", "show", "1"); !strings.Contains(out, "#3 run 500 km") {
		t.Errorf("config path not used:\n%s", out)
	}
}

func TestGoalReaderReportsBadConfig(t *testing.T) {
	r := newRunner(t)
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[goaltracker\ndb = 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.run("", false, "project", "add", "house", "--goal", "3")
	_, errOut := r.runBoth("", false, "--config", cfgPath, "project", "show", "1")
	if !strings.Contains(errOut, "note: config:") || !strings.Contains(errOut, "goal statements not shown") {
		t.Errorf("bad config not reported: %q", errOut)
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
	root := New()
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
	root := New()
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
