package task

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store is the SQLite-backed store for projects, tasks and subtasks.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS areas (
	id         INTEGER PRIMARY KEY,
	name       TEXT NOT NULL,
	parent_id  INTEGER REFERENCES areas(id) ON DELETE SET NULL,
	created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS projects (
	id          INTEGER PRIMARY KEY,
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	state       TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active','done','shelved')),
	area_id     INTEGER REFERENCES areas(id) ON DELETE SET NULL,
	created_at  TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS project_goals (
	project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	goal_id    INTEGER NOT NULL,
	PRIMARY KEY (project_id, goal_id)
);
CREATE TABLE IF NOT EXISTS tasks (
	id         INTEGER PRIMARY KEY,
	project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	title      TEXT NOT NULL,
	status     TEXT NOT NULL DEFAULT 'todo' CHECK (status IN ('todo','doing','done','dropped')),
	due        TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS tasks_project ON tasks(project_id, id);
CREATE TABLE IF NOT EXISTS subtasks (
	id         INTEGER PRIMARY KEY,
	task_id    INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
	title      TEXT NOT NULL,
	done       INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS subtasks_task ON subtasks(task_id, id);
`

// Option adjusts Open.
type Option func(*openOptions)

type openOptions struct {
	ownDir bool
}

// OwnDir says the database's directory belongs to the tool, as the default
// data directory does, so Open may make it owner-only even if it already
// existed. Without it, a pre-existing directory is treated as the user's
// and its mode is left alone.
func OwnDir() Option { return func(o *openOptions) { o.ownDir = true } }

// Open opens or creates the database at path, creating parent directories.
// The file is owner-only: the database holds project descriptions and task
// titles in plaintext, and SQLite reuses the file's mode for the -wal and
// -shm files it keeps beside it. A directory Open creates is owner-only too.
func Open(path string, opts ...Option) (*Store, error) {
	var o openOptions
	for _, opt := range opts {
		opt(&o)
	}
	dir := filepath.Dir(path)
	created := false
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		created = true
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open database file: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("open database file: %w", err)
	}
	if err := restrict(dir, path, created || o.ownDir); err != nil {
		return nil, err
	}
	// The pragmas ride on the DSN so that every connection the pool opens
	// gets them, not just the first. Cascading deletes depend on
	// foreign_keys being on. The path is escaped as a file: URI so a ? in
	// it cannot be taken for the start of the parameters.
	u := url.URL{Path: filepath.ToSlash(path)}
	db, err := sql.Open("sqlite", "file:"+u.EscapedPath()+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// migrate adds what tables made by earlier versions lack. CREATE TABLE IF
// NOT EXISTS leaves an existing table as it was, so a column added to the
// schema later has to be added to old databases here.
func migrate(db *sql.DB) error {
	has, err := hasColumn(db, "projects", "area_id")
	if err != nil {
		return err
	}
	if !has {
		if _, err := db.Exec(`ALTER TABLE projects ADD COLUMN area_id INTEGER REFERENCES areas(id) ON DELETE SET NULL`); err != nil {
			return err
		}
	}
	return nil
}

func hasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// restrict tightens modes left loose by earlier versions. The database and
// its WAL files are always made 0600. The directory is only touched when
// ownDir says so: this call created it, or the caller vouched for it with
// OwnDir. A directory the user chose, such as their home or /tmp, is left
// alone. Windows does not use these modes, so it is skipped.
func restrict(dir, path string, ownDir bool) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	targets := map[string]os.FileMode{path: 0o600, path + "-wal": 0o600, path + "-shm": 0o600}
	if ownDir {
		targets[dir] = 0o700
	}
	for p, mode := range targets {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if info.Mode().Perm() != mode {
			if err := os.Chmod(p, mode); err != nil {
				return fmt.Errorf("restrict %s: %w", p, err)
			}
		}
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// SnapshotTo writes a consistent copy of the database to path using
// VACUUM INTO, which is safe while the database is open and in WAL mode.
// path must not already exist. The copy is owner-only like the original:
// the file is created empty with that mode first, which VACUUM INTO
// accepts as a target.
func (s *Store) SnapshotTo(ctx context.Context, path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("snapshot %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("snapshot %s: %w", path, err)
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		os.Remove(path)
		return fmt.Errorf("snapshot: %w", err)
	}
	return nil
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// tx runs fn inside a transaction.
func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ---- projects ----

// NewProject is the input to AddProject.
type NewProject struct {
	Name        string
	Description string
	AreaID      int64 // 0 for no area
	GoalIDs     []int64
}

// AddProject validates and inserts a project.
func (s *Store) AddProject(ctx context.Context, in NewProject) (Project, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return Project{}, errors.New("name is required")
	}
	goals, err := NormaliseGoalIDs(in.GoalIDs)
	if err != nil {
		return Project{}, err
	}
	if err := s.checkArea(ctx, in.AreaID); err != nil {
		return Project{}, err
	}
	var id int64
	err = s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO projects (name, description, state, area_id, created_at) VALUES (?, ?, ?, ?, ?)`,
			in.Name, strings.TrimSpace(in.Description), string(Active), nullID(in.AreaID), now())
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		return setGoals(ctx, tx, id, goals)
	})
	if err != nil {
		return Project{}, err
	}
	return s.GetProject(ctx, id)
}

// checkArea says whether a project can be put in area id: 0 is no area,
// anything else must exist.
func (s *Store) checkArea(ctx context.Context, id int64) error {
	if id == 0 {
		return nil
	}
	if _, err := s.GetArea(ctx, id); err != nil {
		return fmt.Errorf("area %d: %w", id, err)
	}
	return nil
}

func setGoals(ctx context.Context, tx *sql.Tx, projectID int64, goals []int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM project_goals WHERE project_id = ?`, projectID); err != nil {
		return err
	}
	for _, g := range goals {
		if _, err := tx.ExecContext(ctx, `INSERT INTO project_goals (project_id, goal_id) VALUES (?, ?)`, projectID, g); err != nil {
			return err
		}
	}
	return nil
}

const selectProject = `SELECT id, name, description, state, area_id, created_at FROM projects`

func scanProject(row interface{ Scan(...any) error }) (Project, error) {
	var p Project
	var area sql.NullInt64
	var created string
	if err := row.Scan(&p.ID, &p.Name, &p.Description, &p.State, &area, &created); err != nil {
		return Project{}, err
	}
	p.AreaID = area.Int64
	p.CreatedAt = parseTime(created)
	return p, nil
}

// GetProject returns one project by id, with its goal ids.
func (s *Store) GetProject(ctx context.Context, id int64) (Project, error) {
	p, err := scanProject(s.db.QueryRowContext(ctx, selectProject+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, err
	}
	p.GoalIDs, err = s.goalsFor(ctx, id)
	return p, err
}

func (s *Store) goalsFor(ctx context.Context, projectID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT goal_id FROM project_goals WHERE project_id = ? ORDER BY goal_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var g int64
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) goalsByProject(ctx context.Context) (map[int64][]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project_id, goal_id FROM project_goals ORDER BY project_id, goal_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]int64{}
	for rows.Next() {
		var p, g int64
		if err := rows.Scan(&p, &g); err != nil {
			return nil, err
		}
		out[p] = append(out[p], g)
	}
	return out, rows.Err()
}

// ProjectFilter narrows ListProjects. The zero value lists active projects
// only.
type ProjectFilter struct {
	// All includes done and shelved projects.
	All bool
	// State lists projects in one state only; it overrides All.
	State State
}

// ListProjects returns projects ordered by id.
func (s *Store) ListProjects(ctx context.Context, f ProjectFilter) ([]Project, error) {
	q := selectProject
	var args []any
	switch {
	case f.State != "":
		q += " WHERE state = ?"
		args = append(args, string(f.State))
	case !f.All:
		q += " WHERE state = ?"
		args = append(args, string(Active))
	}
	q += " ORDER BY id"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	goals, err := s.goalsByProject(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].GoalIDs = goals[out[i].ID]
	}
	return out, nil
}

// ProjectEdit holds optional changes; nil fields are left alone.
type ProjectEdit struct {
	Name        *string
	Description *string
	State       *State
	AreaID      *int64   // 0 moves the project out of any area
	GoalIDs     *[]int64 // empty slice clears the links
}

// UpdateProject applies a ProjectEdit.
func (s *Store) UpdateProject(ctx context.Context, id int64, e ProjectEdit) (Project, error) {
	p, err := s.GetProject(ctx, id)
	if err != nil {
		return Project{}, err
	}
	if e.Name != nil {
		v := strings.TrimSpace(*e.Name)
		if v == "" {
			return Project{}, errors.New("name is required")
		}
		p.Name = v
	}
	if e.Description != nil {
		p.Description = strings.TrimSpace(*e.Description)
	}
	if e.State != nil {
		if err := checkState(*e.State); err != nil {
			return Project{}, err
		}
		p.State = *e.State
	}
	if e.AreaID != nil {
		if err := s.checkArea(ctx, *e.AreaID); err != nil {
			return Project{}, err
		}
		p.AreaID = *e.AreaID
	}
	if e.GoalIDs != nil {
		p.GoalIDs, err = NormaliseGoalIDs(*e.GoalIDs)
		if err != nil {
			return Project{}, err
		}
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE projects SET name = ?, description = ?, state = ?, area_id = ? WHERE id = ?`, p.Name, p.Description, string(p.State), nullID(p.AreaID), id); err != nil {
			return err
		}
		if e.GoalIDs != nil {
			return setGoals(ctx, tx, id, p.GoalIDs)
		}
		return nil
	})
	if err != nil {
		return Project{}, err
	}
	return s.GetProject(ctx, id)
}

func checkState(st State) error {
	switch st {
	case Active, Done, Shelved:
		return nil
	}
	return fmt.Errorf("state %q: want active, done or shelved", st)
}

func checkStatus(st Status) error {
	switch st {
	case Todo, Doing, Finished, Dropped:
		return nil
	}
	return fmt.Errorf("status %q: want todo, doing, done or dropped", st)
}

// MarkProject sets a project's state.
func (s *Store) MarkProject(ctx context.Context, id int64, st State) error {
	if err := checkState(st); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE projects SET state = ? WHERE id = ?`, string(st), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteProject removes a project with all its tasks and subtasks.
func (s *Store) DeleteProject(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- tasks ----

// NewTask is the input to AddTask.
type NewTask struct {
	ProjectID int64
	Title     string
	Due       string // anything ParseDue accepts, or empty for none
}

// AddTask validates and inserts a task under a project.
func (s *Store) AddTask(ctx context.Context, in NewTask) (Task, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return Task{}, errors.New("title is required")
	}
	due, err := ParseDue(in.Due, time.Now())
	if err != nil {
		return Task{}, err
	}
	if _, err := s.GetProject(ctx, in.ProjectID); err != nil {
		return Task{}, fmt.Errorf("project %d: %w", in.ProjectID, err)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO tasks (project_id, title, status, due, created_at) VALUES (?, ?, ?, ?, ?)`,
		in.ProjectID, in.Title, string(Todo), due, now())
	if err != nil {
		return Task{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Task{}, err
	}
	return s.GetTask(ctx, id)
}

const selectTask = `SELECT id, project_id, title, status, due, created_at FROM tasks`

func scanTask(row interface{ Scan(...any) error }) (Task, error) {
	var t Task
	var created string
	if err := row.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Status, &t.Due, &created); err != nil {
		return Task{}, err
	}
	t.CreatedAt = parseTime(created)
	return t, nil
}

// GetTask returns one task by id.
func (s *Store) GetTask(ctx context.Context, id int64) (Task, error) {
	t, err := scanTask(s.db.QueryRowContext(ctx, selectTask+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return t, err
}

// TaskFilter narrows ListTasks. Zero values mean no filtering.
type TaskFilter struct {
	ProjectID int64
	Status    Status
	// Open keeps only todo and doing tasks.
	Open bool
	// Due keeps only tasks with a due date, ordered soonest first.
	Due bool
}

// ListTasks returns tasks ordered by project then id, or by due date when
// the filter asks for due tasks.
func (s *Store) ListTasks(ctx context.Context, f TaskFilter) ([]Task, error) {
	q := selectTask
	var where []string
	var args []any
	if f.ProjectID != 0 {
		where = append(where, "project_id = ?")
		args = append(args, f.ProjectID)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, string(f.Status))
	}
	if f.Open {
		where = append(where, "status IN ('todo','doing')")
	}
	if f.Due {
		where = append(where, "due <> ''")
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	if f.Due {
		q += " ORDER BY due, project_id, id"
	} else {
		q += " ORDER BY project_id, id"
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TaskEdit holds optional changes; nil fields are left alone.
type TaskEdit struct {
	Title     *string
	Due       *string // empty clears it
	Status    *Status
	ProjectID *int64 // move the task to another project
}

// UpdateTask applies a TaskEdit.
func (s *Store) UpdateTask(ctx context.Context, id int64, e TaskEdit) (Task, error) {
	t, err := s.GetTask(ctx, id)
	if err != nil {
		return Task{}, err
	}
	if e.Title != nil {
		v := strings.TrimSpace(*e.Title)
		if v == "" {
			return Task{}, errors.New("title is required")
		}
		t.Title = v
	}
	if e.Due != nil {
		t.Due, err = ParseDue(*e.Due, time.Now())
		if err != nil {
			return Task{}, err
		}
	}
	if e.Status != nil {
		if err := checkStatus(*e.Status); err != nil {
			return Task{}, err
		}
		t.Status = *e.Status
	}
	if e.ProjectID != nil {
		if _, err := s.GetProject(ctx, *e.ProjectID); err != nil {
			return Task{}, fmt.Errorf("project %d: %w", *e.ProjectID, err)
		}
		t.ProjectID = *e.ProjectID
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET title = ?, due = ?, status = ?, project_id = ? WHERE id = ?`, t.Title, t.Due, string(t.Status), t.ProjectID, id); err != nil {
		return Task{}, err
	}
	return s.GetTask(ctx, id)
}

// MarkTask sets a task's status.
func (s *Store) MarkTask(ctx context.Context, id int64, st Status) error {
	if err := checkStatus(st); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE tasks SET status = ? WHERE id = ?`, string(st), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteTask removes a task and its subtasks.
func (s *Store) DeleteTask(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tasks WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- subtasks ----

// AddSubtask appends a checklist item to a task.
func (s *Store) AddSubtask(ctx context.Context, taskID int64, title string) (Subtask, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return Subtask{}, errors.New("title is required")
	}
	if _, err := s.GetTask(ctx, taskID); err != nil {
		return Subtask{}, fmt.Errorf("task %d: %w", taskID, err)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO subtasks (task_id, title, done, created_at) VALUES (?, ?, 0, ?)`, taskID, title, now())
	if err != nil {
		return Subtask{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Subtask{}, err
	}
	return s.GetSubtask(ctx, id)
}

const selectSubtask = `SELECT id, task_id, title, done, created_at FROM subtasks`

func scanSubtask(row interface{ Scan(...any) error }) (Subtask, error) {
	var st Subtask
	var created string
	if err := row.Scan(&st.ID, &st.TaskID, &st.Title, &st.Done, &created); err != nil {
		return Subtask{}, err
	}
	st.CreatedAt = parseTime(created)
	return st, nil
}

// GetSubtask returns one subtask by id.
func (s *Store) GetSubtask(ctx context.Context, id int64) (Subtask, error) {
	st, err := scanSubtask(s.db.QueryRowContext(ctx, selectSubtask+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Subtask{}, ErrNotFound
	}
	return st, err
}

// ListSubtasks returns a task's subtasks in the order they were added.
func (s *Store) ListSubtasks(ctx context.Context, taskID int64) ([]Subtask, error) {
	if _, err := s.GetTask(ctx, taskID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, selectSubtask+" WHERE task_id = ? ORDER BY id", taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Subtask
	for rows.Next() {
		st, err := scanSubtask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// TickSubtask sets whether a subtask is done.
func (s *Store) TickSubtask(ctx context.Context, id int64, done bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE subtasks SET done = ? WHERE id = ?`, done, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RenameSubtask changes a subtask's title.
func (s *Store) RenameSubtask(ctx context.Context, id int64, title string) (Subtask, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return Subtask{}, errors.New("title is required")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE subtasks SET title = ? WHERE id = ?`, title, id)
	if err != nil {
		return Subtask{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Subtask{}, ErrNotFound
	}
	return s.GetSubtask(ctx, id)
}

// DeleteSubtask removes a subtask.
func (s *Store) DeleteSubtask(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM subtasks WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- tree ----

// Tree loads every project with its tasks and subtasks, in a fixed number
// of queries however much data there is. Projects, tasks and subtasks are
// each in id order. With all false, done and shelved projects are left out,
// and so are done and dropped tasks and their subtasks.
func (s *Store) Tree(ctx context.Context, all bool) ([]ProjectNode, error) {
	projects, err := s.ListProjects(ctx, ProjectFilter{All: all})
	if err != nil {
		return nil, err
	}
	tasks, err := s.ListTasks(ctx, TaskFilter{Open: !all})
	if err != nil {
		return nil, err
	}
	q := selectSubtask
	if !all {
		q += " WHERE task_id IN (SELECT id FROM tasks WHERE status IN ('todo','doing'))"
	}
	rows, err := s.db.QueryContext(ctx, q+" ORDER BY task_id, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	subs := map[int64][]Subtask{}
	for rows.Next() {
		st, err := scanSubtask(rows)
		if err != nil {
			return nil, err
		}
		subs[st.TaskID] = append(subs[st.TaskID], st)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Every list is a real slice, never nil, so the JSON of the tree has
	// [] rather than null wherever a project or task holds nothing.
	byProject := map[int64][]TaskNode{}
	for _, t := range tasks {
		st := subs[t.ID]
		if st == nil {
			st = []Subtask{}
		}
		byProject[t.ProjectID] = append(byProject[t.ProjectID], TaskNode{Task: t, Subtasks: st})
	}
	out := make([]ProjectNode, 0, len(projects))
	for _, p := range projects {
		ts := byProject[p.ID]
		if ts == nil {
			ts = []TaskNode{}
		}
		out = append(out, ProjectNode{Project: p, Tasks: ts})
	}
	return out, nil
}
