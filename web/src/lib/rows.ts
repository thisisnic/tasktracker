// The rows of the list pane and the rules for where the selection lands,
// ported from the terminal UI so the two agree: the same tree order, the
// same buckets by deadline, the same landing after a change.

import { isOpen, type AreaNode, type Outline, type ProjectNode, type Subtask, type Task, type TaskNode } from "./api";

export type Kind = "area" | "project" | "task" | "subtask" | "heading";

/** A row to select: a kind and an id. A heading's id is its bucket. */
export interface Target {
  kind: Kind;
  id: number;
}

export function sameTarget(a: Target | null, b: Target | null): boolean {
  return !!a && !!b && a.kind === b.kind && a.id === b.id;
}

/** The target as a map key. */
export function targetKey(t: Target): string {
  return `${t.kind}:${t.id}`;
}

export function targetLabel(t: Target): string {
  return `${t.kind} #${t.id}`;
}

export type View = "project" | "deadline";

/** How soon a task is due, for the by-deadline headings. The values are
 * the headings' ids, so they never change: a fold on "the week after
 * next" stays on the week after next as the weeks go by. Overdue, Later
 * and No deadline keep the ids they had before the weeks replaced Next 7
 * days and Next 30 days, so a saved fold on them still means the same. */
export type Bucket = number;
export const OVERDUE: Bucket = 1;
/** Longer, until the weeks. */
export const LATER: Bucket = 4;
export const NONE: Bucket = 5;
/** The week with today in it; the four after it follow. */
export const THIS_WEEK: Bucket = 6;
/** How many weeks get a heading of their own, this week included, before
 * Later takes over. */
export const WEEKS = 5;

/** Whether b is one of the week headings. */
export function isWeek(b: Bucket): boolean {
  return b >= THIS_WEEK && b < THIS_WEEK + WEEKS;
}

/** Whether b is a heading. 2 and 3 were once, Next 7 days and Next 30
 * days; a saved fold on one is dropped rather than landing on a week. */
export function knownBucket(b: Bucket): boolean {
  return b === OVERDUE || b === LATER || b === NONE || isWeek(b);
}

const MONTHS = [
  "January",
  "February",
  "March",
  "April",
  "May",
  "June",
  "July",
  "August",
  "September",
  "October",
  "November",
  "December",
];

/** A day of the month in words: 1st, 2nd, 3rd, 4th, 11th, 21st. */
export function ordinal(n: number): string {
  let suffix = "th";
  if (Math.floor(n / 10) % 10 !== 1) {
    switch (n % 10) {
      case 1:
        suffix = "st";
        break;
      case 2:
        suffix = "nd";
        break;
      case 3:
        suffix = "rd";
        break;
    }
  }
  return `${n}${suffix}`;
}

/** How many days today is into its week: 0 on a Monday, 6 on a Sunday. */
export function sinceMonday(today: string): number {
  return (new Date(`${today}T00:00:00Z`).getUTCDay() + 6) % 7;
}

/** The Monday of the week today is in, YYYY-MM-DD. */
export function weekStart(today: string): string {
  return dayFrom(today, -sinceMonday(today));
}

/** The heading for a bucket. A week is named by its Monday. */
export function bucketName(b: Bucket, today: string): string {
  if (b === OVERDUE) return "Overdue";
  if (isWeek(b)) {
    const monday = new Date(`${dayFrom(weekStart(today), 7 * (b - THIS_WEEK))}T00:00:00Z`);
    return `Week beginning ${ordinal(monday.getUTCDate())} ${MONTHS[monday.getUTCMonth()]}`;
  }
  if (b === LATER) return "Later";
  return "No deadline";
}

/** Today as YYYY-MM-DD in the browser's own zone, which is the zone the
 * machine serving the page keeps, so "today" means the same in the
 * terminal UI. */
export function todayStr(now: Date = new Date()): string {
  const y = now.getFullYear();
  const m = String(now.getMonth() + 1).padStart(2, "0");
  const d = String(now.getDate()).padStart(2, "0");
  return `${y}-${m}-${d}`;
}

/** Days from today to a due date: negative when overdue, zero when due
 * today, null when there is no due date. Both are whole days, so the
 * time of day plays no part. */
export function daysUntil(due: string | undefined, today: string): number | null {
  if (!due) return null;
  const a = Date.parse(`${due}T00:00:00Z`);
  const b = Date.parse(`${today}T00:00:00Z`);
  if (Number.isNaN(a) || Number.isNaN(b)) return null;
  return Math.round((a - b) / 86_400_000);
}

/** A date n days from today, YYYY-MM-DD. */
export function dayFrom(today: string, n: number): string {
  const d = new Date(`${today}T00:00:00Z`);
  d.setUTCDate(d.getUTCDate() + n);
  return d.toISOString().slice(0, 10);
}

/** How soon a due date is: overdue before today, then the week today is
 * in and the four after it, Monday to Sunday, then later. */
export function bucketOf(due: string | undefined, today: string): Bucket {
  const days = daysUntil(due, today);
  if (days === null) return NONE;
  if (days < 0) return OVERDUE;
  // Whole days from Monday to the due date; the week is that over 7.
  const week = Math.floor((days + sinceMonday(today)) / 7);
  return week < WEEKS ? THIS_WEEK + week : LATER;
}

/** Which dates a bucket covers, for the detail pane. This week starts at
 * today, since what was due earlier in it is overdue. */
export function bucketSpan(b: Bucket, today: string): string {
  const monday = weekStart(today);
  if (b === OVERDUE) return "before " + today;
  if (b === THIS_WEEK) return today + " to " + dayFrom(monday, 6);
  if (isWeek(b)) {
    const start = dayFrom(monday, 7 * (b - THIS_WEEK));
    return start + " to " + dayFrom(start, 6);
  }
  if (b === LATER) return "from " + dayFrom(monday, 7 * WEEKS);
  return "no due date";
}

/** Whether a is due before b, with an undated task after any dated one. */
export function sooner(a: Task, b: Task): boolean {
  if (!a.due || !b.due) return !!a.due && !b.due;
  return a.due < b.due;
}

export function overdue(t: Task, today: string): boolean {
  const days = daysUntil(t.due, today);
  return days !== null && isOpen(t) && days < 0;
}

/** How far off a due date is, in words. */
export function dueWords(days: number): string {
  if (days < -1) return `${-days} days overdue`;
  if (days === -1) return "1 day overdue";
  if (days === 0) return "today";
  if (days === 1) return "tomorrow";
  return `in ${days} days`;
}

/** One selectable line in the list pane. area is set for area rows.
 * project is set for every project, task and subtask row: the project
 * the row belongs to. task is set for task and subtask rows. depth is
 * how many areas the row is inside, and sets its indent. A heading row
 * has the bucket it stands for and how many tasks are in it. */
export interface Row {
  kind: Kind;
  depth: number;
  area?: AreaNode;
  project?: ProjectNode;
  task?: TaskNode;
  subtask?: Subtask;
  bucket?: Bucket;
  count?: number;
}

export function rowTarget(r: Row): Target {
  switch (r.kind) {
    case "area":
      return { kind: "area", id: r.area!.area.id };
    case "task":
      return { kind: "task", id: r.task!.task.id };
    case "subtask":
      return { kind: "subtask", id: r.subtask!.id };
    case "heading":
      return { kind: "heading", id: r.bucket! };
    default:
      return { kind: "project", id: r.project!.project.id };
  }
}

/** The by-deadline heading a task or subtask row is under. */
export function headingOf(r: Row, today: string): Target {
  return { kind: "heading", id: bucketOf(r.task!.task.due, today) };
}

/** Something that holds areas and projects: the outline, or an area. */
export interface Holder {
  areas: AreaNode[];
  projects: ProjectNode[];
}

/** Calls fn for every project in h, in tree order: the areas' projects
 * first, however deep, then h's own, until fn returns false. */
export function eachProject(h: Holder, fn: (p: ProjectNode) => boolean | void): void {
  const walk = (n: Holder): boolean => {
    for (const a of n.areas) if (!walk(a)) return false;
    for (const p of n.projects) if (fn(p) === false) return false;
    return true;
  };
  walk(h);
}

/** Calls fn for every task in the outline, in by-project order, until
 * fn returns false. */
export function eachTask(o: Outline, fn: (p: ProjectNode, t: TaskNode) => boolean | void): void {
  eachProject(o, (p) => {
    for (const t of p.tasks) if (fn(p, t) === false) return false;
  });
}

/** The task t names, or the task a subtask t is under. */
export function findTask(o: Outline, t: Target): Task | null {
  let found: Task | null = null;
  eachTask(o, (_, tk) => {
    if (t.kind === "task" && tk.task.id === t.id) {
      found = tk.task;
      return false;
    }
    if (t.kind === "subtask" && tk.subtasks.some((s) => s.id === t.id)) {
      found = tk.task;
      return false;
    }
  });
  return found;
}

/** What t is inside, outermost first: the areas around it, and for a
 * task or subtask its project. Rows that are folded away count, since
 * this is how they are found again. */
export function containers(o: Outline, t: Target): Target[] {
  const walk = (areas: AreaNode[], projects: ProjectNode[], path: Target[]): Target[] | null => {
    for (const a of areas) {
      if (t.kind === "area" && a.area.id === t.id) return path;
      const found = walk(a.areas, a.projects, [...path, { kind: "area", id: a.area.id }]);
      if (found) return found;
    }
    for (const p of projects) {
      if (t.kind === "project" && p.project.id === t.id) return path;
      for (const tk of p.tasks) {
        if (t.kind === "task" && tk.task.id === t.id) return [...path, { kind: "project", id: p.project.id }];
        if (t.kind === "subtask" && tk.subtasks.some((s) => s.id === t.id))
          return [...path, { kind: "project", id: p.project.id }];
      }
    }
    return null;
  };
  return walk(o.areas, o.projects, []) ?? [];
}

/** The heading a task or subtask is under by deadline, or null for
 * anything else and for a finished task, which is not listed there. */
export function deadlineContainer(o: Outline, t: Target, today: string): Target | null {
  if (t.kind !== "task" && t.kind !== "subtask") return null;
  const found = findTask(o, t);
  if (!found || !isOpen(found)) return null;
  return { kind: "heading", id: bucketOf(found.due, today) };
}

/** The rows of the by-project tree: areas, their areas and projects,
 * tasks under projects, subtasks under tasks. A folded area or project
 * keeps what is in it out of the rows. */
export function treeRows(o: Outline, folded: Set<string>): Row[] {
  const rows: Row[] = [];
  const walk = (areas: AreaNode[], projects: ProjectNode[], depth: number) => {
    for (const a of areas) {
      rows.push({ kind: "area", depth, area: a });
      if (folded.has(targetKey({ kind: "area", id: a.area.id }))) continue;
      walk(a.areas, a.projects, depth + 1);
    }
    for (const p of projects) {
      rows.push({ kind: "project", depth, project: p });
      if (folded.has(targetKey({ kind: "project", id: p.project.id }))) continue;
      for (const t of p.tasks) {
        rows.push({ kind: "task", depth, project: p, task: t });
        for (const s of t.subtasks) rows.push({ kind: "subtask", depth, project: p, task: t, subtask: s });
      }
    }
  };
  walk(o.areas, o.projects, 0);
  return rows;
}

/** The rows by deadline: every open task with its subtasks, ordered by
 * due date with undated tasks last, under a heading for each bucket
 * that has any. A folded heading keeps its tasks out of the rows. Done
 * and dropped tasks are not coming up, so they are left out. Tasks due
 * the same day, and undated ones, keep their by-project order, so the
 * list is stable across reloads. */
export function deadlineRows(o: Outline, folded: Set<string>, today: string): Row[] {
  const groups: { due: string | undefined; rows: Row[] }[] = [];
  eachTask(o, (p, t) => {
    if (!isOpen(t.task)) return;
    const rows: Row[] = [{ kind: "task", depth: 0, project: p, task: t }];
    for (const s of t.subtasks) rows.push({ kind: "subtask", depth: 0, project: p, task: t, subtask: s });
    groups.push({ due: t.task.due, rows });
  });
  // A stable sort, so equal dates keep their order.
  groups.sort((a, b) => {
    const ta = { due: a.due } as Task;
    const tb = { due: b.due } as Task;
    if (sooner(ta, tb)) return -1;
    if (sooner(tb, ta)) return 1;
    return 0;
  });
  const counts = new Map<Bucket, number>();
  for (const g of groups) {
    const b = bucketOf(g.due, today);
    counts.set(b, (counts.get(b) ?? 0) + 1);
  }
  const rows: Row[] = [];
  let last: Bucket = 0;
  for (const g of groups) {
    const b = bucketOf(g.due, today);
    if (b !== last) {
      rows.push({ kind: "heading", depth: 0, bucket: b, count: counts.get(b) });
      last = b;
    }
    if (!folded.has(targetKey({ kind: "heading", id: b }))) rows.push(...g.rows);
  }
  return rows;
}

/** The index of the row for t, or -1. */
export function indexOf(rows: Row[], t: Target): number {
  return rows.findIndex((r) => sameTarget(rowTarget(r), t));
}
