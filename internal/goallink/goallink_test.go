package goallink

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// goaltrackerDB makes a database with goaltracker's goals table and a few
// rows, the way goaltracker itself would leave it.
func goaltrackerDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "goaltracker.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`
		CREATE TABLE goals (id INTEGER PRIMARY KEY, statement TEXT NOT NULL, period TEXT NOT NULL, why TEXT NOT NULL DEFAULT '');
		INSERT INTO goals (id, statement, period) VALUES (1, 'run 500 km', '2026'), (2, 'finish the garden', '2026-Q3'), (5, 'swim', '2027');
		PRAGMA journal_mode = WAL;`)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLookupAndAll(t *testing.T) {
	ctx := context.Background()
	r := New(goaltrackerDB(t))
	got, err := r.Lookup(ctx, []int64{2, 9, 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Statement != "finish the garden" || got[1].Statement != "" || got[2].Statement != "run 500 km" {
		t.Errorf("Lookup = %+v", got)
	}
	if got[1].Label() != "#9" || got[0].Label() != "#2 finish the garden" {
		t.Errorf("labels: %q %q", got[1].Label(), got[0].Label())
	}
	all, err := r.All(ctx)
	if err != nil || len(all) != 3 || all[0].ID != 1 || all[2].ID != 5 || all[1].Period != "2026-Q3" {
		t.Errorf("All = %+v, %v", all, err)
	}
	if empty, err := r.Lookup(ctx, nil); err != nil || len(empty) != 0 {
		t.Errorf("Lookup(nil) = %v, %v", empty, err)
	}
}

func TestLookupMissingDatabase(t *testing.T) {
	r := New(filepath.Join(t.TempDir(), "nope.db"))
	got, err := r.Lookup(context.Background(), []int64{3})
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
	if len(got) != 1 || got[0].ID != 3 || got[0].Statement != "" {
		t.Errorf("bare goals = %+v", got)
	}
	if _, err := os.Stat(r.Path()); !os.IsNotExist(err) {
		t.Error("a missing goaltracker database was created")
	}
}

func TestLookupNotAGoalsDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "other.db")
	db, _ := sql.Open("sqlite", path)
	db.Exec(`CREATE TABLE projects (id INTEGER PRIMARY KEY)`)
	db.Close()
	if _, err := New(path).All(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func TestReadOnly(t *testing.T) {
	path := goaltrackerDB(t)
	before, _ := os.Stat(path)
	r := New(path)
	db, err := r.open()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO goals (id, statement, period) VALUES (9, 'x', '2026')`); err == nil {
		t.Error("write succeeded on a read-only connection")
	}
	after, _ := os.Stat(path)
	if before.Size() != after.Size() {
		t.Error("goaltracker database changed")
	}
}

func TestDefaultPath(t *testing.T) {
	t.Setenv("GOALTRACKER_DB", "")
	t.Setenv("XDG_DATA_HOME", "/x")
	if DefaultPath() != "/x/goaltracker/goaltracker.db" {
		t.Errorf("DefaultPath = %s", DefaultPath())
	}
	t.Setenv("GOALTRACKER_DB", "/y/g.db")
	if DefaultPath() != "/y/g.db" {
		t.Errorf("DefaultPath with env = %s", DefaultPath())
	}
}
