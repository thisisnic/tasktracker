package tui

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// FoldsPath is where the folds for the database at dbPath are kept: a
// small file beside it, as SQLite keeps its -wal and -shm files, so each
// database has its own folds and a scratch database never shares them
// with the real one.
func FoldsPath(dbPath string) string { return dbPath + "-folds" }

// foldKinds names the kinds of row that can be folded, as written in the
// folds file.
var foldKinds = map[rowKind]string{rowArea: "area", rowProject: "project", rowHeading: "heading"}

// stamp is how a row's creation time is written in the folds file, and
// compared: the store's own form of it.
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// badLinesError says the folds file was read, but some of its lines were
// not folds and were skipped. Any other error from loadFolds means the
// file was not read through, and what it holds is unknown.
type badLinesError struct {
	path string
	line int    // the first bad line
	text string // what it held
	more int    // how many bad lines followed it
}

func (e *badLinesError) Error() string {
	msg := fmt.Sprintf("folds: %s line %d: %q is not a fold", e.path, e.line, e.text)
	if e.more > 0 {
		msg += fmt.Sprintf(" (and %d more such lines)", e.more)
	}
	return msg
}

// loadFolds reads the folds file at path: one fold per line, as a kind,
// an id and, for an area or project, the stamp of the row's creation.
// The stamp is what tells a row from a later one that took its id, since
// SQLite hands a deleted row's id to the next row made. The result maps
// each fold to its stamp, "" for a heading. A missing file means no
// folds. A line that is not a fold is skipped and reported as a
// badLinesError, with the rest of the file still read, so one bad line
// costs one fold. A fold on a retired heading is dropped without a word:
// the file was right when it was written.
func loadFolds(path string) (map[target]string, error) {
	folds := map[target]string{}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return folds, nil
	}
	if err != nil {
		return folds, fmt.Errorf("folds: %w", err)
	}
	defer f.Close()
	byName := map[string]rowKind{}
	for k, name := range foldKinds {
		byName[name] = k
	}
	var bad *badLinesError
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		t, made, ok := parseFold(line, byName)
		switch {
		case ok && t.kind == rowHeading && bucket(t.id).retired():
		case ok:
			folds[t] = made
		case bad == nil:
			bad = &badLinesError{path: path, line: n, text: line}
		default:
			bad.more++
		}
	}
	if err := sc.Err(); err != nil {
		return folds, fmt.Errorf("folds: %w", err)
	}
	if bad != nil {
		return folds, bad
	}
	return folds, nil
}

// parseFold reads one line of the folds file. A heading's id is its
// bucket, and there are only so many, past ones included; an area's or
// project's is checked against the store on reload, with its stamp.
func parseFold(line string, byName map[string]rowKind) (t target, made string, ok bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return target{}, "", false
	}
	k, known := byName[fields[0]]
	id, err := strconv.ParseInt(fields[1], 10, 64)
	if !known || err != nil || id <= 0 {
		return target{}, "", false
	}
	if k == rowHeading {
		if len(fields) != 2 || !(bucket(id).known() || bucket(id).retired()) {
			return target{}, "", false
		}
		return target{k, id}, "", true
	}
	if len(fields) != 3 {
		return target{}, "", false
	}
	when, err := time.Parse(time.RFC3339, fields[2])
	if err != nil {
		return target{}, "", false
	}
	return target{k, id}, stamp(when), true
}

// saveFolds writes folds to path, replacing what was there. The file is
// written whole, to a temporary file of its own in the same directory,
// which CreateTemp makes owner-only like the database, and renamed into
// place, so a crash part way leaves the old folds rather than half of
// the new, and two UIs on one database cannot rename each other's
// half-written file. Lines are sorted so the same folds always give the
// same file.
func saveFolds(path string, folds map[target]string) (err error) {
	var lines []string
	for t, made := range folds {
		line := fmt.Sprintf("%s %d", foldKinds[t.kind], t.id)
		if t.kind != rowHeading {
			line += " " + made
		}
		lines = append(lines, line+"\n")
	}
	sort.Strings(lines)
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("folds: %w", err)
	}
	defer func() {
		if err != nil {
			os.Remove(f.Name())
		}
	}()
	if _, err := f.WriteString(strings.Join(lines, "")); err != nil {
		f.Close()
		return fmt.Errorf("folds: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("folds: %w", err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("folds: %w", err)
	}
	return nil
}
