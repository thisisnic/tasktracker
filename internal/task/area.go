package task

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Area groups projects, and other areas, above the project level. It is a
// label with a place in a tree: a name and the area it sits in. It has no
// state of its own.
type Area struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	ParentID  int64     `json:"parent_id,omitempty"` // 0 for a top-level area
	CreatedAt time.Time `json:"created_at"`
}

// NewArea is the input to AddArea.
type NewArea struct {
	Name     string
	ParentID int64 // 0 for a top-level area
}

// AddArea validates and inserts an area.
func (s *Store) AddArea(ctx context.Context, in NewArea) (Area, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return Area{}, errors.New("name is required")
	}
	if in.ParentID != 0 {
		if _, err := s.GetArea(ctx, in.ParentID); err != nil {
			return Area{}, fmt.Errorf("area %d: %w", in.ParentID, err)
		}
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO areas (name, parent_id, created_at) VALUES (?, ?, ?)`,
		in.Name, nullID(in.ParentID), now())
	if err != nil {
		return Area{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Area{}, err
	}
	return s.GetArea(ctx, id)
}

// nullID stores 0 as NULL, so a missing parent or area is a real absence
// that foreign keys leave alone.
func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

const selectArea = `SELECT id, name, parent_id, created_at FROM areas`

func scanArea(row interface{ Scan(...any) error }) (Area, error) {
	var a Area
	var parent sql.NullInt64
	var created string
	if err := row.Scan(&a.ID, &a.Name, &parent, &created); err != nil {
		return Area{}, err
	}
	a.ParentID = parent.Int64
	a.CreatedAt = parseTime(created)
	return a, nil
}

// GetArea returns one area by id.
func (s *Store) GetArea(ctx context.Context, id int64) (Area, error) {
	a, err := scanArea(s.db.QueryRowContext(ctx, selectArea+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Area{}, ErrNotFound
	}
	return a, err
}

// ListAreas returns every area ordered by id.
func (s *Store) ListAreas(ctx context.Context) ([]Area, error) {
	rows, err := s.db.QueryContext(ctx, selectArea+" ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Area
	for rows.Next() {
		a, err := scanArea(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AreaEdit holds optional changes; nil fields are left alone. A ParentID
// of 0 moves the area to the top level.
type AreaEdit struct {
	Name     *string
	ParentID *int64
}

// UpdateArea applies an AreaEdit. An area cannot be moved into itself or
// into one of the areas inside it.
func (s *Store) UpdateArea(ctx context.Context, id int64, e AreaEdit) (Area, error) {
	a, err := s.GetArea(ctx, id)
	if err != nil {
		return Area{}, err
	}
	if e.Name != nil {
		v := strings.TrimSpace(*e.Name)
		if v == "" {
			return Area{}, errors.New("name is required")
		}
		a.Name = v
	}
	if e.ParentID != nil {
		if err := s.checkParent(ctx, id, *e.ParentID); err != nil {
			return Area{}, err
		}
		a.ParentID = *e.ParentID
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE areas SET name = ?, parent_id = ? WHERE id = ?`, a.Name, nullID(a.ParentID), id); err != nil {
		return Area{}, err
	}
	return s.GetArea(ctx, id)
}

// checkParent says whether area id can sit in parent: parent must exist
// and must not be id or anything inside id.
func (s *Store) checkParent(ctx context.Context, id, parent int64) error {
	if parent == 0 {
		return nil
	}
	areas, err := s.ListAreas(ctx)
	if err != nil {
		return err
	}
	byID := map[int64]Area{}
	for _, a := range areas {
		byID[a.ID] = a
	}
	if _, ok := byID[parent]; !ok {
		return fmt.Errorf("area %d: %w", parent, ErrNotFound)
	}
	for p := parent; p != 0; p = byID[p].ParentID {
		if p == id {
			return errors.New("an area cannot be put inside itself")
		}
	}
	return nil
}

// DeleteArea removes an area. Whatever was in it, areas and projects
// alike, moves up into the area's parent, or to the top level.
func (s *Store) DeleteArea(ctx context.Context, id int64) error {
	a, err := s.GetArea(ctx, id)
	if err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE areas SET parent_id = ? WHERE parent_id = ?`, nullID(a.ParentID), id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE projects SET area_id = ? WHERE area_id = ?`, nullID(a.ParentID), id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM areas WHERE id = ?`, id)
		return err
	})
}

// AreaPath names an area by its ancestry, outermost first: "home / garden".
// An id that is not in areas gives "".
func AreaPath(areas []Area, id int64) string {
	byID := map[int64]Area{}
	for _, a := range areas {
		byID[a.ID] = a
	}
	var names []string
	seen := map[int64]bool{}
	for a, ok := byID[id]; ok && !seen[a.ID]; a, ok = byID[a.ParentID] {
		seen[a.ID] = true
		names = append([]string{a.Name}, names...)
	}
	return strings.Join(names, " / ")
}

// ---- outline ----

// AreaNode is an area with what is in it, for the tree view: its areas
// first, then its projects, each in id order. Both lists are always
// present in JSON, empty rather than null, so scripts can iterate them.
type AreaNode struct {
	Area     Area          `json:"area"`
	Areas    []AreaNode    `json:"areas"`
	Projects []ProjectNode `json:"projects"`
}

// OpenTasks counts the open tasks in every project inside the area,
// however deep.
func (n AreaNode) OpenTasks() int {
	total := 0
	for _, p := range n.Projects {
		total += p.OpenTasks()
	}
	for _, a := range n.Areas {
		total += a.OpenTasks()
	}
	return total
}

// Counts is how many areas and projects are inside the area, however deep.
func (n AreaNode) Counts() (areas, projects int) {
	areas, projects = len(n.Areas), len(n.Projects)
	for _, a := range n.Areas {
		as, ps := a.Counts()
		areas, projects = areas+as, projects+ps
	}
	return areas, projects
}

// Find returns the node for area id, at any depth.
func (n AreaNode) Find(id int64) (AreaNode, bool) {
	if n.Area.ID == id {
		return n, true
	}
	return findArea(n.Areas, id)
}

func findArea(nodes []AreaNode, id int64) (AreaNode, bool) {
	for _, n := range nodes {
		if found, ok := n.Find(id); ok {
			return found, true
		}
	}
	return AreaNode{}, false
}

// Outline is the whole tree: top-level areas, and projects in no area.
type Outline struct {
	Areas    []AreaNode    `json:"areas"`
	Projects []ProjectNode `json:"projects"`
}

// Find returns the node for area id, at any depth.
func (o Outline) Find(id int64) (AreaNode, bool) { return findArea(o.Areas, id) }

// Outline loads the tree of areas with their projects, tasks and subtasks.
// all is as for Tree: false leaves out finished projects and tasks. Every
// area is included, empty or not, since an area is somewhere to put things.
func (s *Store) Outline(ctx context.Context, all bool) (Outline, error) {
	areas, err := s.ListAreas(ctx)
	if err != nil {
		return Outline{}, err
	}
	projects, err := s.Tree(ctx, all)
	if err != nil {
		return Outline{}, err
	}
	return Nest(areas, projects), nil
}

// Nest arranges areas and projects into an Outline. A project whose area
// is not in areas, or an area whose parent is not, is placed at the top
// level rather than lost.
func Nest(areas []Area, projects []ProjectNode) Outline {
	byID := map[int64]bool{}
	children := map[int64][]Area{}
	for _, a := range areas {
		byID[a.ID] = true
	}
	for _, a := range areas {
		parent := a.ParentID
		if !byID[parent] {
			parent = 0
		}
		children[parent] = append(children[parent], a)
	}
	byArea := map[int64][]ProjectNode{}
	for _, p := range projects {
		area := p.Project.AreaID
		if !byID[area] {
			area = 0
		}
		byArea[area] = append(byArea[area], p)
	}
	projectsIn := func(id int64) []ProjectNode {
		if ps := byArea[id]; ps != nil {
			return ps
		}
		return []ProjectNode{}
	}
	placed := map[int64]bool{}
	var build func(parent int64) []AreaNode
	build = func(parent int64) []AreaNode {
		out := []AreaNode{}
		for _, a := range children[parent] {
			if placed[a.ID] {
				continue
			}
			placed[a.ID] = true
			out = append(out, AreaNode{Area: a, Areas: build(a.ID), Projects: projectsIn(a.ID)})
		}
		return out
	}
	o := Outline{Areas: build(0), Projects: projectsIn(0)}
	// Areas in a cycle are reached from no root. Only a hand-edited
	// database can have one; show them at the top rather than hide them.
	for _, a := range areas {
		if !placed[a.ID] {
			placed[a.ID] = true
			o.Areas = append(o.Areas, AreaNode{Area: a, Areas: build(a.ID), Projects: projectsIn(a.ID)})
		}
	}
	return o
}
