package task

import (
	"reflect"
	"testing"
	"time"
)

func TestParseDue(t *testing.T) {
	// Late evening in a zone well ahead of UTC: "today" must be the local
	// calendar date, not the UTC one.
	loc := time.FixedZone("ahead", 10*3600)
	today := time.Date(2026, 9, 17, 23, 30, 0, 0, loc)
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		{"", "", false},
		{"  none ", "", false},
		{"today", "2026-09-17", false},
		{"Tomorrow", "2026-09-18", false},
		{"2026-10-01", "2026-10-01", false},
		{"2026-13-01", "", true},
		{"next week", "", true},
	}
	for _, c := range cases {
		got, err := ParseDue(c.in, today)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("ParseDue(%q) = %q, %v; want %q, err=%v", c.in, got, err, c.want, c.wantErr)
		}
	}
	if _, err := ParseDue("  Next Week ", today); err == nil || err.Error() != `due "  Next Week ": want YYYY-MM-DD, today, tomorrow or none` {
		t.Errorf("error should quote the input as typed: %v", err)
	}
}

func TestDueArithmetic(t *testing.T) {
	loc := time.FixedZone("ahead", 10*3600)
	today := time.Date(2026, 9, 17, 23, 30, 0, 0, loc)
	cases := []struct {
		due     string
		status  Status
		days    int
		ok      bool
		overdue bool
	}{
		{"", Todo, 0, false, false},
		{"2026-09-17", Todo, 0, true, false},
		{"2026-09-16", Todo, -1, true, true},
		{"2026-09-16", Finished, -1, true, false},
		{"2026-09-16", Dropped, -1, true, false},
		{"2026-09-20", Doing, 3, true, false},
		{"garbage", Todo, 0, false, false},
	}
	for _, c := range cases {
		task := Task{Due: c.due, Status: c.status}
		days, ok := task.DaysUntilDue(today)
		if days != c.days || ok != c.ok {
			t.Errorf("DaysUntilDue(%q) = %d, %v; want %d, %v", c.due, days, ok, c.days, c.ok)
		}
		if got := task.Overdue(today); got != c.overdue {
			t.Errorf("Overdue(%q, %s) = %v, want %v", c.due, c.status, got, c.overdue)
		}
	}
}

func TestParseStateAndStatus(t *testing.T) {
	for in, want := range map[string]State{"active": Active, " Open ": Active, "done": Done, "finished": Done, "shelved": Shelved, "shelve": Shelved} {
		if got, err := ParseState(in); err != nil || got != want {
			t.Errorf("ParseState(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseState("paused"); err == nil {
		t.Error("ParseState accepted paused")
	}
	for in, want := range map[string]Status{"todo": Todo, "to-do": Todo, "DOING": Doing, "in-progress": Doing, "done": Finished, "finished": Finished, "dropped": Dropped, "drop": Dropped} {
		if got, err := ParseStatus(in); err != nil || got != want {
			t.Errorf("ParseStatus(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseStatus("blocked"); err == nil {
		t.Error("ParseStatus accepted blocked")
	}
}

func TestStatusNext(t *testing.T) {
	for from, want := range map[Status]Status{Todo: Doing, Doing: Finished, Finished: Todo, Dropped: Todo} {
		if got := from.Next(); got != want {
			t.Errorf("%s.Next() = %s, want %s", from, got, want)
		}
	}
}

func TestNormaliseGoalIDs(t *testing.T) {
	got, err := NormaliseGoalIDs([]int64{3, 1, 3, 2, 1})
	if err != nil || !reflect.DeepEqual(got, []int64{1, 2, 3}) {
		t.Errorf("NormaliseGoalIDs = %v, %v", got, err)
	}
	if got, err := NormaliseGoalIDs(nil); err != nil || got != nil {
		t.Errorf("nil in should give nil out: %v, %v", got, err)
	}
	for _, bad := range [][]int64{{0}, {1, -2}} {
		if _, err := NormaliseGoalIDs(bad); err == nil {
			t.Errorf("NormaliseGoalIDs(%v) accepted", bad)
		}
	}
}

func TestNodeCounts(t *testing.T) {
	p := ProjectNode{Tasks: []TaskNode{
		{Task: Task{Status: Todo}, Subtasks: []Subtask{{Done: true}, {Done: false}, {Done: true}}},
		{Task: Task{Status: Doing}},
		{Task: Task{Status: Finished}},
		{Task: Task{Status: Dropped}},
	}}
	if p.OpenTasks() != 2 {
		t.Errorf("OpenTasks = %d, want 2", p.OpenTasks())
	}
	if p.Tasks[0].Ticked() != 2 || p.Tasks[1].Ticked() != 0 {
		t.Errorf("Ticked = %d, %d; want 2, 0", p.Tasks[0].Ticked(), p.Tasks[1].Ticked())
	}
	if !(Project{State: Active}).Open() || (Project{State: Shelved}).Open() {
		t.Error("Project.Open wrong")
	}
}
