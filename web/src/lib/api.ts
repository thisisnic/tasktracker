// Typed client for the JSON API the Go binary serves beside this page.
// The shapes are the store's own, as `tasktracker task list --json`
// prints them; empty lists are [] and empty fields are left out.

export type State = "active" | "done" | "shelved";
export type Status = "todo" | "done" | "dropped";

export interface Area {
  id: number;
  name: string;
  parent_id?: number;
  created_at: string;
}

export interface Project {
  id: number;
  name: string;
  description?: string;
  state: State;
  area_id?: number;
  created_at: string;
}

export interface Task {
  id: number;
  project_id: number;
  title: string;
  status: Status;
  due?: string;
  issue?: string;
  notes?: string;
  archived?: boolean;
  created_at: string;
}

export interface Subtask {
  id: number;
  task_id: number;
  title: string;
  done: boolean;
  created_at: string;
}

export interface TaskNode {
  task: Task;
  subtasks: Subtask[];
}

export interface ProjectNode {
  project: Project;
  tasks: TaskNode[];
}

export interface AreaNode {
  area: Area;
  areas: AreaNode[];
  projects: ProjectNode[];
}

export interface Outline {
  areas: AreaNode[];
  projects: ProjectNode[];
}

export interface OutlineResponse {
  /** Names the state of the database; opaque, only ever compared. */
  version: string;
  outline: Outline;
}

export interface Meta {
  version: string;
}

/** The fields of a task an edit or a copy may change. A missing field
 * is left alone; an empty due, issue or notes clears it. */
export interface TaskEdit {
  title?: string;
  due?: string;
  issue?: string;
  notes?: string;
  status?: Status;
  project_id?: number;
}

export interface AreaEdit {
  name?: string;
  parent_id?: number;
}

export interface ProjectEdit {
  name?: string;
  description?: string;
  state?: State;
  area_id?: number;
}

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  // Every mutation declares JSON, body or not: the server refuses the
  // content types a form can send.
  const init: RequestInit = { method, headers: method === "GET" ? {} : { "Content-Type": "application/json" } };
  if (body !== undefined) init.body = JSON.stringify(body);
  const resp = await fetch(path, init);
  if (resp.status === 204) return undefined as T;
  const text = await resp.text();
  let data: unknown = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = null;
  }
  if (!resp.ok) {
    const msg =
      data && typeof data === "object" && "error" in data ? String((data as { error: string }).error) : resp.statusText;
    throw new ApiError(resp.status, msg);
  }
  return data as T;
}

/** The calls the page makes; AppState takes one so tests can hand it a
 * fake over an outline in memory. */
export type Client = typeof api;

export const api = {
  meta: () => call<Meta>("GET", "/api/meta"),
  version: () => call<{ version: string }>("GET", "/api/version"),
  outline: (all: boolean) => call<OutlineResponse>("GET", `/api/outline${all ? "?all=1" : ""}`),
  projects: () => call<Project[]>("GET", "/api/projects"),

  addArea: (name: string, parent_id: number) => call<Area>("POST", "/api/areas", { name, parent_id }),
  editArea: (id: number, patch: AreaEdit) => call<Area>("PATCH", `/api/areas/${id}`, patch),
  deleteArea: (id: number) => call<void>("DELETE", `/api/areas/${id}`),

  addProject: (name: string, description: string, area_id: number) =>
    call<Project>("POST", "/api/projects", { name, description, area_id }),
  editProject: (id: number, patch: ProjectEdit) => call<Project>("PATCH", `/api/projects/${id}`, patch),
  deleteProject: (id: number) => call<void>("DELETE", `/api/projects/${id}`),

  addTask: (project_id: number, title: string, due: string, issue: string, notes: string) =>
    call<Task>("POST", "/api/tasks", { project_id, title, due, issue, notes }),
  editTask: (id: number, patch: TaskEdit) => call<Task>("PATCH", `/api/tasks/${id}`, patch),
  archiveTask: (id: number, archived: boolean) => call<Task>("PUT", `/api/tasks/${id}/archived`, { archived }),
  archiveFinished: () => call<{ archived: number }>("POST", "/api/tasks/archive-finished"),
  copyTask: (id: number, patch: TaskEdit) => call<TaskNode>("POST", `/api/tasks/${id}/copy`, patch),
  deleteTask: (id: number) => call<void>("DELETE", `/api/tasks/${id}`),

  addSubtask: (task_id: number, title: string) => call<Subtask>("POST", `/api/tasks/${task_id}/subtasks`, { title }),
  renameSubtask: (id: number, title: string) => call<Subtask>("PATCH", `/api/subtasks/${id}`, { title }),
  tickSubtask: (id: number, done: boolean) => call<Subtask>("PUT", `/api/subtasks/${id}/done`, { done }),
  deleteSubtask: (id: number) => call<void>("DELETE", `/api/subtasks/${id}`),
};

/** Whether the task is still to be done. */
export function isOpen(t: Task): boolean {
  return t.status === "todo";
}

/** The status after s when stepping a task: todo to done and back.
 * Dropped steps back to todo. */
export function nextStatus(s: Status): Status {
  return s === "todo" ? "done" : "todo";
}

/** The state after s when stepping a project: active, done, shelved,
 * then back to active. */
export function nextState(s: State): State {
  switch (s) {
    case "active":
      return "done";
    case "done":
      return "shelved";
    default:
      return "active";
  }
}

/** The open tasks in a project. */
export function openTasks(p: ProjectNode): number {
  return p.tasks.filter((t) => isOpen(t.task)).length;
}

/** The open tasks in every project inside an area, however deep. */
export function areaOpenTasks(a: AreaNode): number {
  return a.projects.reduce((n, p) => n + openTasks(p), 0) + a.areas.reduce((n, b) => n + areaOpenTasks(b), 0);
}

/** How many areas and projects are inside an area, however deep. */
export function areaCounts(a: AreaNode): { areas: number; projects: number } {
  let areas = a.areas.length;
  let projects = a.projects.length;
  for (const b of a.areas) {
    const c = areaCounts(b);
    areas += c.areas;
    projects += c.projects;
  }
  return { areas, projects };
}

/** The ticked subtasks of a task. */
export function ticked(t: TaskNode): number {
  return t.subtasks.filter((s) => s.done).length;
}

/** Every area in the outline, flat, in id order: for paths and pick lists. */
export function flatAreas(o: Outline): Area[] {
  const out: Area[] = [];
  const walk = (nodes: AreaNode[]) => {
    for (const n of nodes) {
      out.push(n.area);
      walk(n.areas);
    }
  };
  walk(o.areas);
  return out.sort((a, b) => a.id - b.id);
}

/** An area named by its ancestry, outermost first: "home / garden". An
 * id that is not in areas gives "". */
export function areaPath(areas: Area[], id: number): string {
  const byId = new Map(areas.map((a) => [a.id, a]));
  const names: string[] = [];
  const seen = new Set<number>();
  for (let a = byId.get(id); a && !seen.has(a.id); a = byId.get(a.parent_id ?? 0)) {
    seen.add(a.id);
    names.unshift(a.name);
  }
  return names.join(" / ");
}

/** The areas inside an area, itself included: what it cannot be moved into. */
export function inside(n: AreaNode): Set<number> {
  const out = new Set<number>([n.area.id]);
  for (const a of n.areas) for (const id of inside(a)) out.add(id);
  return out;
}

/** The task's issue link as owner/repo#N and as the URL to open, or
 * null when there is none or it is not a GitHub issue URL, as from a
 * hand-edited database; the server stores only what ParseIssue accepts,
 * so a stored link that does not match is shown as text. */
export function issueRef(issue: string | undefined): { ref: string; link: string } | null {
  if (!issue) return null;
  const m = /^https:\/\/github\.com\/([A-Za-z0-9-]+)\/([A-Za-z0-9._-]+)\/(issues|pull)\/([0-9]+)$/.exec(issue);
  if (!m) return null;
  return { ref: `${m[1]}/${m[2]}#${m[4]}`, link: issue };
}
