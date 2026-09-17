// Package goallink reads goal statements from goaltracker's database so a
// project can show the goals it serves. The database is opened read-only
// and never changed. Nothing here is required: when the database cannot be
// read, callers show goal ids on their own.
package goallink

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Goal is a goaltracker goal as far as tasktracker cares: its id and
// statement. Statement is empty when the goal could not be found.
type Goal struct {
	ID        int64  `json:"id"`
	Statement string `json:"statement,omitempty"`
	Period    string `json:"period,omitempty"`
}

// Label is how a goal reads in a list: "#3 run 500 km", or just "#3" when
// the statement is unknown.
func (g Goal) Label() string {
	if g.Statement == "" {
		return fmt.Sprintf("#%d", g.ID)
	}
	return fmt.Sprintf("#%d %s", g.ID, g.Statement)
}

// DefaultPath is where goaltracker keeps its database unless told
// otherwise: $GOALTRACKER_DB, or $XDG_DATA_HOME/goaltracker/goaltracker.db,
// falling back to ~/.local/share/goaltracker/goaltracker.db.
func DefaultPath() string {
	if p := os.Getenv("GOALTRACKER_DB"); p != "" {
		return p
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "goaltracker", "goaltracker.db")
}

// ErrUnavailable wraps any reason the goaltracker database could not be
// read: missing file, unreadable file, or a database without a goals table.
var ErrUnavailable = errors.New("goaltracker database unavailable")

// Reader looks goals up in goaltracker's database.
type Reader struct {
	path string
}

// New returns a Reader for the database at path. Nothing is opened until
// a lookup.
func New(path string) *Reader { return &Reader{path: path} }

// Path is the database the reader looks in.
func (r *Reader) Path() string { return r.path }

// open opens the database read-only. A missing file is an error rather
// than a new empty database.
func (r *Reader) open() (*sql.DB, error) {
	if _, err := os.Stat(r.path); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	// A file: URI so SQLite honours mode=ro; the path is escaped the way
	// a URI path is, keeping slashes but not ? or #.
	u := url.URL{Path: filepath.ToSlash(r.path)}
	db, err := sql.Open("sqlite", "file:"+u.EscapedPath()+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// All lists every goal in goaltracker, in period then id order, for
// choosing which ones a project serves.
func (r *Reader) All(ctx context.Context) ([]Goal, error) {
	db, err := r.open()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT id, statement, period FROM goals ORDER BY period, id`)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer rows.Close()
	var out []Goal
	for rows.Next() {
		var g Goal
		if err := rows.Scan(&g.ID, &g.Statement, &g.Period); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return out, nil
}

// Lookup returns a Goal for each id, in the order given. Ids that are not
// in goaltracker come back with an empty statement. When the database
// cannot be read at all, every goal comes back bare and the error says why;
// callers may show the bare list and mention the error, or ignore it.
func (r *Reader) Lookup(ctx context.Context, ids []int64) ([]Goal, error) {
	out := make([]Goal, len(ids))
	for i, id := range ids {
		out[i] = Goal{ID: id}
	}
	if len(ids) == 0 {
		return out, nil
	}
	all, err := r.All(ctx)
	if err != nil {
		return out, err
	}
	byID := make(map[int64]Goal, len(all))
	for _, g := range all {
		byID[g.ID] = g
	}
	for i, id := range ids {
		if g, ok := byID[id]; ok {
			out[i] = g
		}
	}
	return out, nil
}
