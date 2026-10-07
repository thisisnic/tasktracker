import { describe, expect, it } from "vitest";
import type { Outline } from "./api";
import {
  LONGER,
  MONTH,
  NONE,
  OVERDUE,
  WEEK,
  bucketOf,
  bucketSpan,
  containers,
  daysUntil,
  deadlineContainer,
  deadlineRows,
  dueWords,
  findTask,
  rowTarget,
  targetKey,
  treeRows,
} from "./rows";
import { fixture } from "./fixture";

const today = "2026-10-07";

// The rows as "kind:id" strings, for comparing whole lists.
function keys(rows: ReturnType<typeof treeRows>): string[] {
  return rows.map((r) => targetKey(rowTarget(r)));
}

describe("treeRows", () => {
  it("lists areas, their areas, then projects, with tasks and subtasks under them", () => {
    expect(keys(treeRows(fixture(), new Set()))).toEqual([
      "area:1",
      "area:2",
      "project:2",
      "task:3",
      "project:1",
      "task:1",
      "subtask:1",
      "subtask:2",
      "task:2",
      "project:3",
      "task:4",
      "task:5",
    ]);
  });

  it("keeps what is inside a folded area or project out of the rows", () => {
    expect(keys(treeRows(fixture(), new Set(["area:2"])))).toEqual([
      "area:1",
      "area:2",
      "project:1",
      "task:1",
      "subtask:1",
      "subtask:2",
      "task:2",
      "project:3",
      "task:4",
      "task:5",
    ]);
    expect(keys(treeRows(fixture(), new Set(["project:1"])))).toContain("task:3");
    expect(keys(treeRows(fixture(), new Set(["project:1"])))).not.toContain("task:1");
  });

  it("sets the depth from the areas a row is inside", () => {
    const rows = treeRows(fixture(), new Set());
    const depth = Object.fromEntries(rows.map((r) => [targetKey(rowTarget(r)), r.depth]));
    expect(depth).toMatchObject({ "area:1": 0, "area:2": 1, "project:2": 2, "task:3": 2, "project:1": 1, "project:3": 0 });
  });
});

describe("deadlineRows", () => {
  it("orders open tasks by due date, undated last, under headings with counts", () => {
    // Tree order is task 3, 1, 2, 4, 5; due order disagrees: 1 (overdue),
    // 2 (this week), 3 (longer), then the undated 4. Task 5 is done and
    // not listed.
    const rows = deadlineRows(fixture(), new Set(), today);
    expect(keys(rows)).toEqual([
      "heading:1",
      "task:1",
      "subtask:1",
      "subtask:2",
      "heading:2",
      "task:2",
      "heading:4",
      "task:3",
      "heading:5",
      "task:4",
    ]);
    expect(rows.filter((r) => r.kind === "heading").map((r) => r.count)).toEqual([1, 1, 1, 1]);
  });

  it("keeps tasks due the same day in tree order", () => {
    const o = fixture();
    o.projects[0]!.tasks[0]!.task.due = "2026-10-09"; // task 4, now due with task 2
    const rows = deadlineRows(o, new Set(), today);
    expect(keys(rows).filter((k) => k.startsWith("task:"))).toEqual(["task:1", "task:2", "task:4", "task:3"]);
  });

  it("keeps a folded heading's tasks out of the rows, and its count", () => {
    const rows = deadlineRows(fixture(), new Set([targetKey({ kind: "heading", id: OVERDUE })]), today);
    expect(keys(rows).slice(0, 2)).toEqual(["heading:1", "heading:2"]);
    expect(rows[0]!.count).toBe(1);
  });

  it("has no rows when nothing is open", () => {
    const o = fixture();
    for (const p of [...o.projects, ...o.areas.flatMap((a) => [...a.projects, ...a.areas.flatMap((b) => b.projects)])])
      for (const t of p.tasks) t.task.status = "done";
    expect(deadlineRows(o, new Set(), today)).toEqual([]);
  });
});

describe("buckets", () => {
  it("puts the next 7 days from today and the next 30 after them", () => {
    expect(bucketOf("2026-10-06", today)).toBe(OVERDUE);
    expect(bucketOf("2026-10-07", today)).toBe(WEEK);
    expect(bucketOf("2026-10-13", today)).toBe(WEEK);
    expect(bucketOf("2026-10-14", today)).toBe(MONTH);
    expect(bucketOf("2026-11-05", today)).toBe(MONTH);
    expect(bucketOf("2026-11-06", today)).toBe(LONGER);
    expect(bucketOf(undefined, today)).toBe(NONE);
    expect(bucketOf("", today)).toBe(NONE);
  });

  it("says which dates each covers", () => {
    expect(bucketSpan(OVERDUE, today)).toBe("before 2026-10-07");
    expect(bucketSpan(WEEK, today)).toBe("2026-10-07 to 2026-10-13");
    expect(bucketSpan(MONTH, today)).toBe("2026-10-14 to 2026-11-05");
    expect(bucketSpan(LONGER, today)).toBe("from 2026-11-06");
    expect(bucketSpan(NONE, today)).toBe("no due date");
  });

  it("counts whole days and puts them in words", () => {
    expect(daysUntil("2026-10-05", today)).toBe(-2);
    expect(daysUntil("2026-10-07", today)).toBe(0);
    expect(daysUntil("2026-10-08", today)).toBe(1);
    expect(daysUntil(undefined, today)).toBeNull();
    expect(daysUntil("nonsense", today)).toBeNull();
    expect(dueWords(-2)).toBe("2 days overdue");
    expect(dueWords(-1)).toBe("1 day overdue");
    expect(dueWords(0)).toBe("today");
    expect(dueWords(1)).toBe("tomorrow");
    expect(dueWords(9)).toBe("in 9 days");
  });
});

describe("containers", () => {
  it("lists what a row is inside, outermost first", () => {
    const o = fixture();
    expect(containers(o, { kind: "task", id: 3 })).toEqual([
      { kind: "area", id: 1 },
      { kind: "area", id: 2 },
      { kind: "project", id: 2 },
    ]);
    expect(containers(o, { kind: "subtask", id: 2 })).toEqual([
      { kind: "area", id: 1 },
      { kind: "project", id: 1 },
    ]);
    expect(containers(o, { kind: "project", id: 3 })).toEqual([]);
    expect(containers(o, { kind: "area", id: 2 })).toEqual([{ kind: "area", id: 1 }]);
    expect(containers(o, { kind: "task", id: 99 })).toEqual([]);
  });

  it("names the heading an open task is under by deadline, and none for a finished one", () => {
    const o = fixture();
    expect(deadlineContainer(o, { kind: "subtask", id: 1 }, today)).toEqual({ kind: "heading", id: OVERDUE });
    expect(deadlineContainer(o, { kind: "task", id: 4 }, today)).toEqual({ kind: "heading", id: NONE });
    expect(deadlineContainer(o, { kind: "task", id: 5 }, today)).toBeNull();
    expect(deadlineContainer(o, { kind: "project", id: 1 }, today)).toBeNull();
  });

  it("finds the task a subtask is under", () => {
    expect(findTask(fixture(), { kind: "subtask", id: 2 })?.id).toBe(1);
    expect(findTask(fixture(), { kind: "task", id: 99 })).toBeNull();
  });
});

describe("fixture", () => {
  it("is a fresh outline each time", () => {
    const a: Outline = fixture();
    a.projects.length = 0;
    expect(fixture().projects.length).toBe(1);
  });
});
