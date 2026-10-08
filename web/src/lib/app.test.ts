import { describe, expect, it } from "vitest";
import type { Client, Outline, Project, Status, Task } from "./api";
import { AppState } from "./app.svelte";
import { eachProject, eachTask, rowTarget, targetKey, type Target } from "./rows";
import { fixture } from "./fixture";

// A client over an outline in memory, with the writes the actions make.
// Every call answers a copy, as the server would, so the page never
// holds the fake's own objects.
class Fake {
  data = fixture();
  /** The server process, which the test changes for a restart. */
  epoch = 1;
  /** The page build the fake server says it serves, and the binary. */
  page = "p1";
  binary = "test";
  /** SQLite's data version, which the test moves for a write elsewhere. */
  version = 1;
  /** The server's own write count, which the fake's writes move. */
  writes = 0;
  calls: string[] = [];
  /** When set, outline answers wait here until the test lets them go,
   * in whatever order it likes. */
  hold = false;
  held: (() => void)[] = [];
  /** When set, the version cannot be read, as when the server is down. */
  down = false;
  /** When set, version answers wait here until the test lets them go. */
  holdVersion = false;
  heldVersions: (() => void)[] = [];

  private task(id: number): Task {
    let found: Task | null = null;
    eachTask(this.data, (_, t) => {
      if (t.task.id === id) found = t.task;
    });
    if (!found) throw new Error("not found");
    return found;
  }

  private copy(): Outline {
    return JSON.parse(JSON.stringify(this.data)) as Outline;
  }

  /** The outline as the server lists it: archived tasks only with all. */
  private listed(all: boolean): Outline {
    const o = this.copy();
    if (all) return o;
    const prune = (projects: Outline["projects"]) => {
      for (const p of projects) p.tasks = p.tasks.filter((t) => !t.task.archived);
    };
    const walk = (areas: Outline["areas"]) => {
      for (const a of areas) {
        prune(a.projects);
        walk(a.areas);
      }
    };
    prune(o.projects);
    walk(o.areas);
    return o;
  }

  /** The version as the server reports it: data version, then writes. */
  private v(): string {
    return `${this.epoch}.${this.version}.${this.writes}`;
  }

  client(): Client {
    const notImplemented = () => Promise.reject(new Error("not in the fake"));
    return {
      meta: async () => ({ version: this.binary, page: this.page, epoch: String(this.epoch) }),
      version: async () => {
        if (this.down) throw new Error("Failed to fetch");
        if (this.holdVersion) await new Promise<void>((r) => this.heldVersions.push(r));
        return { version: this.v() };
      },
      outline: async (all: boolean) => {
        if (this.hold) await new Promise<void>((r) => this.held.push(r));
        return { version: this.v(), outline: this.listed(all) };
      },
      projects: async () => {
        const out: Project[] = [];
        eachProject(this.data, (p) => {
          out.push({ ...p.project });
        });
        return out.sort((a, b) => a.id - b.id);
      },
      addArea: async (name: string, parent_id: number) => {
        this.calls.push(`addArea ${name} ${parent_id}`);
        this.writes++;
        return { id: 9, name, parent_id, created_at: "" };
      },
      editArea: async (id: number, patch: object) => {
        this.calls.push(`editArea ${id} ${JSON.stringify(patch)}`);
        this.writes++;
        return { id, name: "x", created_at: "" };
      },
      deleteArea: notImplemented,
      addProject: async (name: string, description: string, area_id: number) => {
        this.calls.push(`addProject ${JSON.stringify({ name, description, area_id })}`);
        this.writes++;
        return { id: 9, name, description, state: "active" as const, area_id, created_at: "" };
      },
      editProject: async (id: number, patch: object) => {
        this.calls.push(`editProject ${id} ${JSON.stringify(patch)}`);
        this.writes++;
        return { id, name: "x", state: "active" as const, created_at: "" };
      },
      deleteProject: notImplemented,
      addTask: async (project_id: number, title: string, due: string, issue: string, notes: string) => {
        this.calls.push(`addTask ${JSON.stringify({ project_id, title, due, issue, notes })}`);
        this.writes++;
        return { id: 9, project_id, title, status: "todo" as const, created_at: "" };
      },
      editTask: async (id: number, patch: { status?: Status }) => {
        this.calls.push(`editTask ${id} ${JSON.stringify(patch)}`);
        this.writes++;
        const t = this.task(id);
        if (patch.status) t.status = patch.status;
        if (t.status === "todo") t.archived = false;
        return { ...t };
      },
      archiveFinished: async () => {
        this.calls.push("archiveFinished");
        this.writes++;
        let archived = 0;
        eachTask(this.data, (p, t) => {
          if (p.project.state === "active" && !t.task.archived && t.task.status !== "todo") {
            t.task.archived = true;
            archived++;
          }
        });
        return { archived };
      },
      archiveTask: async (id: number, archived: boolean) => {
        this.calls.push(`archiveTask ${id} ${archived}`);
        this.writes++;
        const t = this.task(id);
        t.archived = archived;
        return { ...t };
      },
      copyTask: async (id: number, patch: object) => {
        this.calls.push(`copyTask ${id} ${JSON.stringify(patch)}`);
        this.writes++;
        const task = { ...this.task(id), id: 9, status: "todo" as const };
        return { task, subtasks: [1, 2].map((n) => ({ id: 10 + n, task_id: 9, title: `s${n}`, done: false, created_at: "" })) };
      },
      deleteTask: async (id: number) => {
        this.calls.push(`deleteTask ${id}`);
        this.writes++;
        const drop = (projects: Outline["projects"]) => {
          for (const p of projects) p.tasks = p.tasks.filter((t) => t.task.id !== id);
        };
        const walk = (areas: Outline["areas"]) => {
          for (const a of areas) {
            drop(a.projects);
            walk(a.areas);
          }
        };
        drop(this.data.projects);
        walk(this.data.areas);
      },
      addSubtask: async (task_id: number, title: string) => {
        this.calls.push(`addSubtask ${task_id} ${title}`);
        this.writes++;
        return { id: 9, task_id, title, done: false, created_at: "" };
      },
      renameSubtask: async (id: number, title: string) => {
        this.calls.push(`renameSubtask ${id} ${title}`);
        this.writes++;
        return { id, task_id: 1, title, done: false, created_at: "" };
      },
      tickSubtask: async (id: number, done: boolean) => {
        this.calls.push(`tickSubtask ${id} ${done}`);
        this.writes++;
        return { id, task_id: 1, title: "x", done, created_at: "" };
      },
      deleteSubtask: notImplemented,
    };
  }
}

// open loads the fixture into a page whose clock is pinned to
// 2026-10-07, which the fixture's dates are arranged around. clock.date
// can be moved.
async function open(): Promise<{ app: AppState; fake: Fake; clock: { date: string } }> {
  const fake = new Fake();
  const clock = { date: "2026-10-07" };
  const app = new AppState(fake.client(), () => new Date(`${clock.date}T12:00:00`));
  // What start would do without the poll: the rows and the page served.
  await app.reload();
  app.page = fake.page;
  return { app, fake, clock };
}

/** Finishes a task behind the page's back, as the CLI would. */
function finish(fake: Fake, id: number) {
  eachTask(fake.data, (_, t) => {
    if (t.task.id === id) t.task.status = "done";
  });
}

function key(t: Target | null): string {
  return t ? targetKey(t) : "none";
}

function selected(app: AppState): string {
  return key(app.selectedTarget());
}

function selectKey(app: AppState, k: string) {
  const [kind, id] = k.split(":");
  if (!app.selectTarget({ kind: kind as Target["kind"], id: Number(id) })) throw new Error(`${k} is not listed`);
}

describe("switching views", () => {
  it("keeps the selection on an open task or subtask, which has a row in both", async () => {
    const { app } = await open();
    selectKey(app, "task:2");
    app.toggleView();
    expect(app.view).toBe("deadline");
    expect(selected(app)).toBe("task:2");
    app.toggleView();
    expect(app.view).toBe("project");
    expect(selected(app)).toBe("task:2");
    selectKey(app, "subtask:2");
    app.toggleView();
    expect(selected(app)).toBe("subtask:2");
  });

  it("goes from a project to its soonest due task listed", async () => {
    const { app } = await open();
    selectKey(app, "project:2");
    app.toggleView();
    expect(selected(app)).toBe("task:3");
  });

  it("goes from an area to the soonest due task in any project inside it", async () => {
    const { app } = await open();
    selectKey(app, "area:1");
    app.toggleView();
    // Tree order inside home is task 3 first; due order says task 1.
    expect(selected(app)).toBe("task:1");
  });

  it("goes from a finished task to the soonest due open task in its project", async () => {
    const { app } = await open();
    selectKey(app, "task:5");
    app.toggleView();
    expect(selected(app)).toBe("task:4");
  });

  it("goes to the heading hiding the soonest due task when it is folded away", async () => {
    const { app } = await open();
    app.folds = { "heading:5": "" };
    selectKey(app, "project:3");
    app.toggleView();
    expect(selected(app)).toBe("heading:5");
  });

  it("goes to the top from a project with nothing open", async () => {
    const { app, fake } = await open();
    for (const t of fake.data.projects[0]!.tasks) t.task.status = "dropped";
    await app.reload();
    selectKey(app, "project:3");
    app.toggleView();
    expect(app.cursor).toBe(0);
    expect(selected(app)).toBe("heading:1");
  });

  it("lands at once, with nothing asked of the server", async () => {
    const { app, fake } = await open();
    selectKey(app, "task:2");
    fake.hold = true;
    app.toggleView();
    expect(app.view).toBe("deadline");
    expect(selected(app)).toBe("task:2");
    expect(fake.held.length).toBe(0);
    selectKey(app, "heading:4");
    app.toggleView();
    expect(selected(app)).toBe("task:3");
    expect(fake.held.length).toBe(0);
  });

  it("goes back from a heading to the first task due about as soon, in tree order", async () => {
    const { app } = await open();
    app.toggleView();
    selectKey(app, "heading:2");
    app.toggleView();
    expect(selected(app)).toBe("task:2");
    app.toggleView();
    selectKey(app, "heading:1");
    app.folds = { "project:1": "2026-01-03T00:00:00Z" };
    app.toggleView();
    // Task 1 is folded away under house, so its project is selected.
    expect(selected(app)).toBe("project:1");
  });
});

describe("finishing a task", () => {
  it("by deadline drops the row and lands on the next task, not the heading that slid in", async () => {
    const { app, fake } = await open();
    app.toggleView();
    selectKey(app, "task:2"); // alone under Next 7 days; Longer's heading follows
    await app.advance();
    expect(fake.calls).toEqual(['editTask 2 {"status":"done"}']);
    expect(app.status).toBe("task #2 done (gone from this list; by project shows it until archived)");
    expect(selected(app)).toBe("task:3");
  });

  it("by project keeps the row, greyed, and says z archives it", async () => {
    const { app } = await open();
    selectKey(app, "task:2");
    await app.advance();
    expect(app.status).toBe("task #2 done; z archives it");
    expect(selected(app)).toBe("task:2");
  });

  it("by deadline lands on the task before when the last one goes", async () => {
    const { app } = await open();
    app.toggleView();
    selectKey(app, "task:4"); // the last row
    await app.advance();
    expect(selected(app)).toBe("task:3");
  });
});

describe("stepping a project", () => {
  it("asks before marking it done, which hides it", async () => {
    const { app, fake } = await open();
    selectKey(app, "project:1");
    await app.advance();
    const m = app.modal;
    if (m?.kind !== "confirmState") throw new Error(`modal is ${m?.kind}`);
    expect(app.stateQuestion(m.row)).toBe("mark done project #1 “house” and hide it with every task in it?");
    await app.confirm(true);
    expect(fake.calls).toEqual(['editProject 1 {"state":"done"}']);
    expect(app.status).toBe("project #1 done (hidden; f shows finished)");
  });
});

describe("archiving", () => {
  it("hides the task and keeps the cursor's place", async () => {
    const { app, fake } = await open();
    selectKey(app, "task:5");
    const index = app.cursor;
    await app.archive();
    expect(fake.calls).toEqual(["archiveTask 5 true"]);
    expect(app.status).toBe("task #5 archived (hidden; f shows archived)");
    // The last row went, so the cursor is clamped to the new last row.
    expect(app.cursor).toBe(index - 1);
    expect(selected(app)).toBe("task:4");
  });

  it("refuses an open task with a hint for the view", async () => {
    const { app, fake } = await open();
    selectKey(app, "task:2");
    await app.archive();
    expect(fake.calls).toEqual([]);
    expect(app.status).toBe("task #2 is still todo; space finishes it or x drops it, then z archives it");
    app.toggleView();
    await app.archive();
    expect(app.status).toBe("task #2 is still todo; space finishes it or x drops it, then z in by project archives it");
  });

  it("shows archived tasks with f, and says what that means by deadline", async () => {
    const { app, fake } = await open();
    fake.data.projects[0]!.tasks[1]!.task.archived = true;
    await app.reload();
    expect(app.rows.map((r) => key(rowTarget(r)))).not.toContain("task:5");
    await app.toggleShowAll();
    expect(app.status).toBe("showing archived tasks and finished projects");
    expect(app.rows.map((r) => key(rowTarget(r)))).toContain("task:5");
    app.toggleView();
    await app.toggleShowAll();
    expect(app.status).toBe(
      "hiding archived tasks and finished projects; here that drops open tasks in finished projects",
    );
  });
});

describe("archiving every finished task", () => {
  it("puts the done and dropped ones away and lands the cursor near", async () => {
    const { app, fake } = await open();
    fake.data.areas[0]!.projects[0]!.tasks[1]!.task.status = "dropped";
    await app.reload();
    selectKey(app, "task:5"); // the last row
    await app.archiveFinished();
    expect(fake.calls).toEqual(["archiveFinished"]);
    expect(app.status).toBe("archived 2 finished tasks (hidden; f shows archived)");
    const keys = app.rows.map((r) => key(rowTarget(r)));
    expect(keys).not.toContain("task:2");
    expect(keys).not.toContain("task:5");
    expect(selected(app)).toBe("task:4");
  });

  it("says so when there is nothing to put away", async () => {
    const { app, fake } = await open();
    fake.data.projects[0]!.tasks[1]!.task.status = "todo";
    await app.reload();
    await app.archiveFinished();
    expect(fake.calls).toEqual(["archiveFinished"]);
    expect(app.status).toBe("no finished tasks to archive");
  });

  it("keeps the rows listed with archived shown, and says nothing about f", async () => {
    const { app } = await open();
    await app.toggleShowAll();
    selectKey(app, "task:5");
    await app.archiveFinished();
    expect(app.status).toBe("archived 1 finished task");
    expect(selected(app)).toBe("task:5");
    expect(app.rows.find((r) => key(rowTarget(r)) === "task:5")?.task?.task.archived).toBe(true);
  });

  it("leaves a finished task in a finished project where it is", async () => {
    const { app, fake } = await open();
    fake.data.projects[0]!.project.state = "shelved"; // admin, with done task 5
    fake.data.areas[0]!.projects[0]!.tasks[1]!.task.status = "dropped"; // task 2
    await app.toggleShowAll();
    selectKey(app, "task:5");
    await app.archiveFinished();
    expect(app.status).toBe("archived 1 finished task");
    expect(selected(app)).toBe("task:5");
    expect(app.rows.find((r) => key(rowTarget(r)) === "task:5")?.task?.task.archived).toBeFalsy();
    expect(app.rows.find((r) => key(rowTarget(r)) === "task:2")?.task?.task.archived).toBe(true);
  });

  it("by deadline says where the archived tasks are listed", async () => {
    const { app } = await open();
    app.toggleView();
    selectKey(app, "task:2");
    await app.archiveFinished();
    expect(app.status).toBe("archived 1 finished task (by project lists them with f)");
    expect(selected(app)).toBe("task:2");
  });

  it("by deadline with archived shown does not name f, which would hide them", async () => {
    const { app, fake } = await open();
    await app.toggleShowAll();
    app.toggleView();
    fake.data.projects[0]!.tasks[0]!.task.status = "dropped";
    await app.reload();
    await app.archiveFinished();
    expect(app.status).toBe("archived 2 finished tasks (by project lists them)");
  });
});

describe("the checkbox", () => {
  it("ticks a task done, with space's message", async () => {
    const { app, fake } = await open();
    selectKey(app, "task:2");
    await app.tick();
    expect(fake.calls).toEqual(['editTask 2 {"status":"done"}']);
    expect(app.status).toBe("task #2 done; z archives it");
    expect(selected(app)).toBe("task:2");
  });

  it("unticks a done task back to todo", async () => {
    const { app, fake } = await open();
    selectKey(app, "task:5");
    await app.tick();
    expect(fake.calls).toEqual(['editTask 5 {"status":"todo"}']);
    expect(app.status).toBe("task #5 todo");
  });

  it("ticks a dropped task done, where space would reopen it", async () => {
    const { app, fake } = await open();
    fake.data.projects[0]!.tasks[0]!.task.status = "dropped";
    await app.reload();
    selectKey(app, "task:4");
    await app.tick();
    expect(fake.calls).toEqual(['editTask 4 {"status":"done"}']);
    expect(app.status).toBe("task #4 done; z archives it");
    fake.data.projects[0]!.tasks[0]!.task.status = "dropped";
    await app.reload();
    await app.advance();
    expect(fake.calls).toEqual(['editTask 4 {"status":"done"}', 'editTask 4 {"status":"todo"}']);
    expect(app.status).toBe("task #4 todo");
  });

  it("leaves an archived dropped task archived when ticked done", async () => {
    const { app, fake } = await open();
    const t = fake.data.projects[0]!.tasks[1]!.task;
    t.status = "dropped";
    t.archived = true;
    await app.toggleShowAll();
    selectKey(app, "task:5");
    await app.tick();
    expect(fake.calls).toEqual(['editTask 5 {"status":"done"}']);
    expect(app.status).toBe("task #5 done (still archived)");
    expect(app.rows.find((r) => key(rowTarget(r)) === "task:5")?.task?.task.archived).toBe(true);
  });

  it("toggles a subtask", async () => {
    const { app, fake } = await open();
    selectKey(app, "subtask:2");
    await app.tick();
    expect(fake.calls).toEqual(["tickSubtask 2 true"]);
    expect(app.status).toBe("ticked subtask #2");
  });
});

describe("deleting", () => {
  it("asks first, then lands on the row that takes the deleted row's place", async () => {
    const { app, fake } = await open();
    selectKey(app, "task:1");
    app.requestDelete();
    const m = app.modal;
    if (m?.kind !== "confirmDelete") throw new Error(`modal is ${m?.kind}`);
    expect(app.deleteQuestion(m.row)).toBe("delete task #1 “paint the hall” and its subtasks?");
    await app.confirm(true);
    expect(fake.calls).toEqual(["deleteTask 1"]);
    expect(app.status).toBe("deleted task #1");
    expect(app.modal).toBeNull();
    expect(selected(app)).toBe("task:2");
  });

  it("keeps the row when answered no", async () => {
    const { app, fake } = await open();
    selectKey(app, "task:1");
    app.requestDelete();
    await app.confirm(false);
    expect(fake.calls).toEqual([]);
    expect(app.status).toBe("kept");
    expect(selected(app)).toBe("task:1");
  });
});

describe("folding", () => {
  it("folds the project a task is in and selects the project", async () => {
    const { app } = await open();
    selectKey(app, "task:1");
    app.toggleFold();
    expect(app.status).toBe("collapsed house; ← shows its tasks again");
    expect(selected(app)).toBe("project:1");
    expect(app.folds).toEqual({ "project:1": "2026-01-03T00:00:00Z" });
    app.toggleFold();
    expect(app.status).toBe("expanded house");
    expect(app.folds).toEqual({});
  });

  it("says when there is nothing to hide", async () => {
    const { app, fake } = await open();
    fake.data.projects[0]!.tasks = [];
    await app.reload();
    selectKey(app, "project:3");
    app.toggleFold();
    expect(app.status).toBe("nothing to hide: the project has no tasks listed");
    expect(app.folds).toEqual({});
  });

  it("by deadline folds the heading a task is under", async () => {
    const { app } = await open();
    app.toggleView();
    selectKey(app, "subtask:1");
    app.toggleFold();
    expect(app.status).toBe("collapsed Overdue; ← shows its tasks again");
    expect(selected(app)).toBe("heading:1");
  });

  it("is undone around a row that was just saved, so it can be shown", async () => {
    const { app } = await open();
    app.folds = { "area:1": "2026-01-01T00:00:00Z", "project:1": "2026-01-03T00:00:00Z" };
    await app.saved({ kind: "task", id: 2 }, "saved task #2");
    expect(app.folds).toEqual({});
    expect(selected(app)).toBe("task:2");
    expect(app.status).toBe("saved task #2");
  });

  it("drops a fold whose row's id now belongs to a row made at another time, and keeps one whose row is not listed", async () => {
    const { app } = await open();
    app.folds = { "project:1": "2025-12-31T00:00:00Z", "project:99": "2026-01-01T00:00:00Z", "heading:1": "" };
    await app.reload();
    expect(app.folds).toEqual({ "project:99": "2026-01-01T00:00:00Z", "heading:1": "" });
  });
});

describe("a click on a row", () => {
  it("opens the row's form", async () => {
    const { app } = await open();
    selectKey(app, "task:2");
    app.open();
    expect(app.modal?.kind).toBe("task");
    app.cancelForm();
    selectKey(app, "subtask:2");
    app.open();
    expect(app.modal?.kind).toBe("subtask");
    app.cancelForm();
    selectKey(app, "project:1");
    app.open();
    expect(app.modal?.kind).toBe("project");
  });

  it("opens the detail of a heading, which has no form", async () => {
    const { app } = await open();
    app.toggleView();
    app.first();
    expect(app.selected()?.kind).toBe("heading");
    app.open();
    expect(app.modal).toBeNull();
    expect(app.drawer).toBe(true);
  });

  it("only selects while a write is in flight, for a heading too", async () => {
    const { app, fake } = await open();
    selectKey(app, "task:2");
    fake.hold = true;
    const pressed = app.advance();
    await new Promise((r) => setTimeout(r, 0));
    app.open();
    expect(app.modal).toBeNull();
    expect(app.drawer).toBe(false);
    app.toggleView();
    app.first();
    app.open();
    expect(app.drawer).toBe(false);
    fake.held[0]!();
    await pressed;
    // The message was made when space was pressed, by project.
    expect(app.status).toBe("task #2 done; z archives it");
  });
});

describe("the drawer", () => {
  it("opens and closes with enter, and follows the selection as the keys move it", async () => {
    const { app } = await open();
    selectKey(app, "task:1");
    app.toggleDrawer();
    expect(app.drawer).toBe(true);
    app.toggleDrawer();
    expect(app.drawer).toBe(false);
    app.toggleDrawer();
    app.move(1);
    expect(app.drawer).toBe(true);
    expect(selected(app)).toBe("subtask:1");
    app.toggleView();
    expect(app.drawer).toBe(true);
    app.closeDrawer();
    expect(app.drawer).toBe(false);
  });

  it("closes when the row it showed is hidden by a write of the page's own", async () => {
    const { app } = await open();
    selectKey(app, "task:5");
    app.openDrawer();
    await app.archive();
    expect(app.status).toBe("task #5 archived (hidden; f shows archived)");
    expect(app.drawer).toBe(false);
    // A write that keeps the row listed keeps the drawer.
    selectKey(app, "task:2");
    app.openDrawer();
    await app.advance();
    expect(selected(app)).toBe("task:2");
    expect(app.drawer).toBe(true);
  });

  it("closes when a task finished by deadline leaves the list", async () => {
    const { app } = await open();
    app.toggleView();
    selectKey(app, "task:2");
    app.openDrawer();
    await app.advance();
    expect(selected(app)).toBe("task:3");
    expect(app.drawer).toBe(false);
  });

  it("closes when a change from elsewhere takes the row away", async () => {
    const { app, fake } = await open();
    selectKey(app, "task:4");
    app.openDrawer();
    fake.version = 2;
    fake.data.projects[0]!.tasks = [];
    await app.poll();
    expect(app.drawer).toBe(false);
    expect(app.status).toBe("changed elsewhere, reloaded");
  });

  it("closes when a fold hides the row", async () => {
    const { app } = await open();
    selectKey(app, "task:1");
    app.openDrawer();
    app.toggleFold();
    expect(selected(app)).toBe("project:1");
    expect(app.drawer).toBe(false);
  });

  it("stays open on a row saved into a fold, which the save unfolds", async () => {
    const { app, fake } = await open();
    app.folds = { "project:2": "2026-01-04T00:00:00Z" };
    selectKey(app, "task:1");
    app.openDrawer();
    // Moved into the folded maintenance project; tree order puts it
    // before house's tasks now.
    fake.data.areas[0]!.projects[0]!.tasks[0]!.task.project_id = 2;
    const [moved] = fake.data.areas[0]!.projects[0]!.tasks.splice(0, 1);
    fake.data.areas[0]!.areas[0]!.projects[0]!.tasks.push(moved!);
    await app.saved({ kind: "task", id: 1 }, "saved task #1");
    expect(app.folds).toEqual({});
    expect(selected(app)).toBe("task:1");
    expect(app.drawer).toBe(true);
    expect(app.status).toBe("saved task #1");

    // By deadline, into a folded bucket: task 2 goes from this week to
    // Longer, which is folded.
    app.toggleView();
    app.folds = { "heading:4": "" };
    expect(app.rows.map((r) => key(rowTarget(r)))).not.toContain("task:3");
    selectKey(app, "task:2");
    app.openDrawer();
    fake.data.areas[0]!.projects[0]!.tasks[0]!.task.due = "2026-12-02";
    await app.saved({ kind: "task", id: 2 }, "saved task #2");
    expect(app.folds).toEqual({});
    expect(selected(app)).toBe("task:2");
    expect(app.drawer).toBe(true);
  });

  it("closes on a delete, and opens again when the delete was refused", async () => {
    const { app, fake } = await open();
    selectKey(app, "task:1");
    app.openDrawer();
    app.requestDelete();
    await app.confirm(true);
    expect(app.drawer).toBe(false);
    expect(fake.calls).toEqual(["deleteTask 1"]);

    const page = new AppState({ ...fake.client(), deleteTask: () => Promise.reject(new Error("locked")) }, () => new Date("2026-10-07T12:00:00"));
    await page.reload();
    selectKey(page, "task:2");
    page.openDrawer();
    page.requestDelete();
    await page.confirm(true);
    expect(page.drawer).toBe(true);
    expect(selected(page)).toBe("task:2");
    expect(page.error).toBe("locked");
    expect(page.status).toBe("");
  });

  it("stays closed when a refused delete finds the row already gone elsewhere", async () => {
    const { fake } = await open();
    const page = new AppState({ ...fake.client(), deleteTask: () => Promise.reject(new Error("not found")) }, () => new Date("2026-10-07T12:00:00"));
    await page.reload();
    selectKey(page, "task:2");
    page.openDrawer();
    // The CLI deleted task 2 before the question was answered; the
    // refusal's reload drops it and the selection keeps its place.
    fake.data.areas[0]!.projects[0]!.tasks.pop();
    page.requestDelete();
    await page.confirm(true);
    expect(page.error).toBe("not found");
    expect(selected(page)).toBe("project:3");
    expect(page.drawer).toBe(false);
  });
});

describe("the first load", () => {
  it("is tried again until the server answers", async () => {
    const fake = new Fake();
    let calls = 0;
    const client = {
      ...fake.client(),
      meta: async () => {
        if (++calls === 1) throw new Error("Failed to fetch");
        return { version: "test", page: "p1", epoch: "1" };
      },
    };
    const app = new AppState(client, () => new Date("2026-10-07T12:00:00"));
    await app.start();
    expect(app.fatal).toBe("Failed to fetch");
    expect(app.ready).toBe(false);
    // What the timer does next.
    await app.start();
    expect(app.fatal).toBe("");
    expect(app.ready).toBe(true);
    expect(app.version).toBe("test");
    expect(app.rows.length).toBe(12);
  });
});

describe("a write that failed", () => {
  it("still reloads, since it may have landed", async () => {
    const { app, fake } = await open();
    selectKey(app, "task:2");
    const client = fake.client();
    const flaky = new AppState(
      {
        ...client,
        editTask: async (id, patch) => {
          await client.editTask(id, patch);
          throw new Error("read back failed");
        },
      },
      () => new Date("2026-10-07T12:00:00"),
    );
    await flaky.reload();
    selectKey(flaky, "task:2");
    await flaky.advance();
    expect(flaky.error).toBe("read back failed");
    expect(flaky.rows.find((r) => key(rowTarget(r)) === "task:2")?.task?.task.status).toBe("done");
    expect(flaky.inFlight).toBe(false);
  });
});

describe("a second press while a write is in flight", () => {
  it("is dropped, so a task is not stepped from a status it no longer has", async () => {
    const { app, fake } = await open();
    selectKey(app, "task:2");
    fake.hold = true;
    const first = app.advance();
    await new Promise((r) => setTimeout(r, 0));
    await app.advance();
    expect(fake.calls).toEqual(['editTask 2 {"status":"done"}']);
    fake.held[0]!();
    await first;
    expect(app.status).toBe("task #2 done; z archives it");
    fake.hold = false;
    await app.advance();
    expect(fake.calls).toEqual(['editTask 2 {"status":"done"}', 'editTask 2 {"status":"todo"}']);
    expect(app.status).toBe("task #2 todo");
  });
});

describe("saving a form", () => {
  it("sends only the task fields that changed, keeping a stored issue out of it", async () => {
    const { app, fake } = await open();
    selectKey(app, "task:1");
    app.edit();
    const m = app.modal;
    if (m?.kind !== "task") throw new Error(`modal is ${m?.kind}`);
    const f = app.taskFields(m);
    expect(f).toEqual({ title: "paint the hall", due: "2026-09-10", issue: "", notes: "two coats", status: "todo", project: 1 });
    await app.saveTask(m, { ...f, title: "paint the stairs" });
    expect(fake.calls).toEqual(['editTask 1 {"title":"paint the stairs"}']);
    expect(app.status).toBe("saved task #1");
    expect(app.modal).toBeNull();
    // Nothing changed, nothing sent; the status still says saved.
    await app.saveTask(m, f);
    expect(fake.calls.length).toBe(1);
    expect(app.status).toBe("saved task #1");
  });

  it("starts a copy from the original without its issue, and says what came with it", async () => {
    const { app, fake } = await open();
    fake.data.areas[0]!.projects[0]!.tasks[0]!.task.issue = "https://github.com/o/r/issues/4";
    await app.reload();
    selectKey(app, "task:1");
    app.copyTask();
    const m = app.modal;
    if (m?.kind !== "task") throw new Error(`modal is ${m?.kind}`);
    const f = app.taskFields(m);
    expect(f).toEqual({ title: "paint the hall", due: "2026-09-10", issue: "", notes: "two coats", status: "todo", project: 1 });
    await app.saveTask(m, { ...f, project: 2 });
    expect(fake.calls).toEqual(['copyTask 1 {"title":"paint the hall","due":"2026-09-10","issue":"","notes":"two coats","project_id":2}']);
    expect(app.status).toBe("copied task #1 to task #9 with its 2 subtasks, unticked");
  });

  it("offers every project for the task form, finished ones included", async () => {
    const { app, fake } = await open();
    fake.data.projects[0]!.project.state = "shelved";
    expect((await app.projects()).map((p) => `${p.name} ${p.state}`)).toEqual([
      "house active",
      "maintenance active",
      "admin shelved",
    ]);
  });

  it("adds a task under the selected row's project", async () => {
    const { app, fake } = await open();
    selectKey(app, "task:3");
    app.newTask();
    const m = app.modal;
    if (m?.kind !== "task") throw new Error(`modal is ${m?.kind}`);
    await app.saveTask(m, { ...app.taskFields(m), title: "weed the beds", due: "tomorrow" });
    expect(fake.calls).toEqual(['addTask {"project_id":2,"title":"weed the beds","due":"tomorrow","issue":"","notes":""}']);
    expect(app.status).toBe("saved task #9");
  });

  it("sends only the project fields that changed", async () => {
    const { app, fake } = await open();
    selectKey(app, "project:1");
    app.edit();
    const m = app.modal;
    if (m?.kind !== "project") throw new Error(`modal is ${m?.kind}`);
    await app.saveProject(m, { name: "house", description: "", areaId: 1, state: "active" });
    expect(fake.calls).toEqual([]);
    await app.saveProject(m, { name: "house", description: "fix it up", areaId: 0, state: "shelved" });
    expect(fake.calls).toEqual(['editProject 1 {"description":"fix it up","area_id":0,"state":"shelved"}']);
    expect(app.status).toBe("saved project #1");
    app.newProject();
    const n = app.modal;
    if (n?.kind !== "project") throw new Error(`modal is ${n?.kind}`);
    // The saved project is selected, so a new one goes in its area.
    expect(n.areaId).toBe(1);
    await app.saveProject(n, { name: "garage", description: "", areaId: 2, state: "active" });
    expect(fake.calls[1]).toBe('addProject {"name":"garage","description":"","area_id":2}');
    expect(app.status).toBe("saved project #9");
  });

  it("sends only the area fields that changed, and adds under the selected area", async () => {
    const { app, fake } = await open();
    selectKey(app, "area:2");
    app.edit();
    const m = app.modal;
    if (m?.kind !== "area") throw new Error(`modal is ${m?.kind}`);
    expect([...m.blocked]).toEqual([2]);
    await app.saveArea(m, "garden", 1);
    expect(fake.calls).toEqual([]);
    await app.saveArea(m, "back garden", 0);
    expect(fake.calls).toEqual(['editArea 2 {"name":"back garden","parent_id":0}']);
    expect(app.status).toBe("saved area #2");
    selectKey(app, "task:3");
    app.newArea();
    const n = app.modal;
    if (n?.kind !== "area") throw new Error(`modal is ${n?.kind}`);
    expect(n.parentId).toBe(2);
    await app.saveArea(n, "shed", n.parentId);
    expect(fake.calls[1]).toBe("addArea shed 2");
    expect(app.status).toBe("saved area #9");
  });

  it("that the server refused leaves the form open, and cancelling it says so alone", async () => {
    const { app, fake } = await open();
    selectKey(app, "task:1");
    app.edit();
    const m = app.modal;
    if (m?.kind !== "task") throw new Error(`modal is ${m?.kind}`);
    const refusing = { ...fake.client(), editTask: () => Promise.reject(new Error('due "soon": want YYYY-MM-DD, today, tomorrow or none')) };
    const page = new AppState(refusing, () => new Date("2026-10-07T12:00:00"));
    await page.reload();
    selectKey(page, "task:1");
    page.edit();
    const pm = page.modal;
    if (pm?.kind !== "task") throw new Error(`modal is ${pm?.kind}`);
    await expect(page.saveTask(pm, { ...page.taskFields(pm), due: "soon" })).rejects.toThrow("want YYYY-MM-DD");
    expect(page.modal).toBe(pm);
    page.cancelForm();
    // The refusal wrote nothing, so the server's version stands, and
    // the poll has nothing to note after "cancelled".
    await page.poll();
    expect(page.status).toBe("cancelled");
  });

  it("adds and renames a subtask under the selected task", async () => {
    const { app, fake } = await open();
    selectKey(app, "subtask:2");
    app.newSubtask();
    const m = app.modal;
    if (m?.kind !== "subtask") throw new Error(`modal is ${m?.kind}`);
    expect(m.under.id).toBe(1);
    await app.saveSubtask(m, "sand the door");
    expect(fake.calls).toEqual(["addSubtask 1 sand the door"]);
    expect(app.status).toBe("saved subtask #9");
    selectKey(app, "subtask:2");
    app.edit();
    const e = app.modal;
    if (e?.kind !== "subtask") throw new Error(`modal is ${e?.kind}`);
    await app.saveSubtask(e, "move the furniture");
    expect(fake.calls[1]).toBe("renameSubtask 2 move the furniture");
    expect(app.status).toBe("saved subtask #2");
  });
});

describe("midnight", () => {
  it("moves the date on at the next poll, with no write needed, and keeps the selection", async () => {
    const { app, fake, clock } = await open();
    app.toggleView();
    expect(app.rows.map((r) => key(rowTarget(r)))).toEqual([
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
    selectKey(app, "task:3");
    // Three days on, task 2 is overdue; a poll with nothing written
    // moves it.
    clock.date = "2026-10-10";
    const before = fake.version;
    await app.poll();
    expect(fake.version).toBe(before);
    expect(app.today).toBe("2026-10-10");
    expect(app.rows.map((r) => key(rowTarget(r)))).toEqual([
      "heading:1",
      "task:1",
      "subtask:1",
      "subtask:2",
      "task:2",
      "heading:4",
      "task:3",
      "heading:5",
      "task:4",
    ]);
    expect(selected(app)).toBe("task:3");
  });
});

describe("changes from elsewhere", () => {
  it("are noted once after the last action's message", async () => {
    const { app, fake } = await open();
    fake.version = 2;
    await app.poll();
    expect(app.status).toBe("changed elsewhere, reloaded");
    app.status = "task #2 done; z archives it";
    fake.version = 3;
    await app.poll();
    expect(app.status).toBe("task #2 done; z archives it; changed elsewhere, reloaded");
    fake.version = 4;
    await app.poll();
    expect(app.status).toBe("task #2 done; z archives it; changed elsewhere, reloaded");
    await app.poll();
    expect(app.status).toBe("task #2 done; z archives it; changed elsewhere, reloaded");
    // r says it reloaded, then notes the change.
    fake.version = 5;
    await app.refreshNow();
    expect(app.status).toBe("reloaded; changed elsewhere, reloaded");
  });

  it("made while a form was open are noted after what the form saved", async () => {
    const { app, fake } = await open();
    selectKey(app, "task:1");
    app.edit();
    expect(app.modal?.kind).toBe("task");
    // The CLI writes while the form is open: the poll leaves the form
    // alone, and the save's own reload takes the change in and says so.
    fake.version = 2;
    finish(fake, 2);
    await app.poll();
    expect(app.rows.find((r) => key(rowTarget(r)) === "task:2")?.task?.task.status).toBe("todo");
    await app.saved({ kind: "task", id: 1 }, "saved task #1");
    expect(app.status).toBe("saved task #1; changed elsewhere, reloaded");
    expect(app.rows.find((r) => key(rowTarget(r)) === "task:2")?.task?.task.status).toBe("done");
    // The server's own write count moving on its own is this page's
    // doing, or another tab's, and is not noted after an own write.
    fake.writes++;
    await app.saved({ kind: "task", id: 1 }, "saved task #1");
    expect(app.status).toBe("saved task #1");
  });

  it("cannot have a form open under f's reload, whose answer always lands", async () => {
    const { app, fake } = await open();
    fake.data.projects[0]!.tasks[1]!.task.archived = true;
    await app.reload();
    selectKey(app, "task:4");
    fake.hold = true;
    const toggled = app.toggleShowAll();
    await new Promise((r) => setTimeout(r, 0));
    app.edit();
    expect(app.modal).toBeNull();
    fake.held[0]!();
    await toggled;
    expect(app.showAll).toBe(true);
    expect(app.rows.map((r) => key(rowTarget(r)))).toContain("task:5");
    expect(app.status).toBe("showing archived tasks and finished projects");
  });

  it("leave f as it was when its reload fails", async () => {
    const { app, fake } = await open();
    const failing = new AppState(
      { ...fake.client(), outline: () => Promise.reject(new Error("Failed to fetch")) },
      () => new Date("2026-10-07T12:00:00"),
    );
    failing.outline = app.outline;
    await failing.toggleShowAll();
    expect(failing.showAll).toBe(false);
    expect(failing.error).toBe("Failed to fetch");
  });

  it("made while the server was down are taken in once it is back", async () => {
    const { app, fake } = await open();
    // The server restarts with the same counts; what the CLI wrote in
    // between is in the database, and SQLite's data version on the new
    // connection does not say so.
    fake.epoch = 2;
    finish(fake, 2);
    await app.poll();
    expect(app.rows.find((r) => key(rowTarget(r)) === "task:2")?.task?.task.status).toBe("done");
    expect(app.status).toBe("changed elsewhere, reloaded");
  });

  it("made by a server serving another page build make the page load itself afresh", async () => {
    const fake = new Fake();
    let restarted = 0;
    const app = new AppState(fake.client(), () => new Date("2026-10-07T12:00:00"), () => restarted++);
    await app.start();
    expect(app.version).toBe("test");
    // A restart of the same build is a reload, as above; the top bar
    // takes the new binary's version, as a release without a page
    // change serves the same build.
    fake.epoch = 2;
    fake.binary = "v2";
    await app.poll();
    expect(restarted).toBe(0);
    expect(app.status).toBe("changed elsewhere, reloaded");
    expect(app.version).toBe("v2");
    // A restart serving another page build: this page's code is stale,
    // so it is loaded afresh rather than reloaded.
    fake.epoch = 3;
    fake.page = "p2";
    finish(fake, 2);
    await app.poll();
    expect(restarted).toBe(1);
    expect(app.rows.find((r) => key(rowTarget(r)) === "task:2")?.task?.task.status).toBe("todo");
  });

  it("before the first load's meta answer make the page load itself afresh", async () => {
    const fake = new Fake();
    let restarted = 0;
    // The page knows its own build, as the server names it in index.html.
    const same = new AppState(fake.client(), () => new Date("2026-10-07T12:00:00"), () => restarted++, "p1");
    await same.start();
    expect(restarted).toBe(0);
    expect(same.ready).toBe(true);
    // A page from an earlier build, answered by a server on this one.
    const stale = new AppState(fake.client(), () => new Date("2026-10-07T12:00:00"), () => restarted++, "p0");
    await stale.start();
    expect(restarted).toBe(1);
    expect(stale.ready).toBe(false);
    expect(stale.fatal).toBe("");
  });

  it("between the first load's two requests still make the page load itself afresh", async () => {
    // A page that does not know its own build, as under the dev server.
    const fake = new Fake();
    let restarted = 0;
    const client = fake.client();
    const app = new AppState(
      {
        ...client,
        meta: async () => {
          const meta = await client.meta();
          // The server restarts serving another build after the old
          // one's meta has been answered and before the outline is asked.
          fake.epoch = 2;
          fake.page = "p2";
          return meta;
        },
      },
      () => new Date("2026-10-07T12:00:00"),
      () => restarted++,
    );
    await app.start();
    expect(app.page).toBe("p1");
    expect(restarted).toBe(0);
    // The poll sees a process other than the one the build came from,
    // and the build served now is another one.
    await app.poll();
    expect(restarted).toBe(1);
  });

  it("make the page load itself afresh even when its own reload saw the restart first", async () => {
    const fake = new Fake();
    let restarted = 0;
    const app = new AppState(fake.client(), () => new Date("2026-10-07T12:00:00"), () => restarted++);
    await app.start();
    fake.epoch = 2;
    fake.page = "p2";
    await app.refresh(); // r: the rows now carry the new server's version
    await app.poll();
    expect(restarted).toBe(1);
  });

  it("are noted after f, which reloads", async () => {
    const { app, fake } = await open();
    fake.version = 2;
    await app.toggleShowAll();
    expect(app.status).toBe("showing archived tasks and finished projects; changed elsewhere, reloaded");
  });

  it("are not noted for the page's own writes", async () => {
    const { app } = await open();
    selectKey(app, "task:2");
    await app.advance();
    expect(app.status).toBe("task #2 done; z archives it");
    await app.saved({ kind: "task", id: 2 }, "saved task #2");
    expect(app.status).toBe("saved task #2");
  });

  it("cannot put an older answer over a newer one", async () => {
    const { app, fake } = await open();
    selectKey(app, "task:2");
    fake.hold = true;
    // A poll's reload is in flight when space is pressed; the write
    // lands, and its reload is answered first. The poll's answer, from
    // before the write, must then be dropped.
    const polled = app.reload();
    const pressed = app.advance();
    await new Promise((r) => setTimeout(r, 0));
    expect(fake.held.length).toBe(2);
    fake.held[1]!();
    await pressed;
    expect(app.status).toBe("task #2 done; z archives it");
    fake.held[0]!();
    await polled;
    expect(app.status).toBe("task #2 done; z archives it");
    const t = app.rows.find((r) => key(rowTarget(r)) === "task:2")?.task?.task;
    expect(t?.status).toBe("done");
    expect(selected(app)).toBe("task:2");
  });

  it("are not polled for while the page's own write is in flight", async () => {
    const { app, fake } = await open();
    selectKey(app, "task:2");
    fake.hold = true;
    const pressed = app.advance();
    await new Promise((r) => setTimeout(r, 0));
    expect(fake.held.length).toBe(1);
    // The version has moved with the write, but the poll leaves the
    // write's own reload to land it.
    await app.poll();
    expect(fake.held.length).toBe(1);
    fake.held[0]!();
    await pressed;
    expect(app.status).toBe("task #2 done; z archives it");
    expect(selected(app)).toBe("task:2");
    // Afterwards the poll looks again, and with the write's version
    // already loaded, has nothing to do.
    fake.hold = false;
    await app.poll();
    expect(app.status).toBe("task #2 done; z archives it");
  });

  it("are not acted on by a poll that was answering when the page wrote", async () => {
    const { app, fake } = await open();
    selectKey(app, "task:2");
    fake.holdVersion = true;
    const polled = app.poll();
    await new Promise((r) => setTimeout(r, 0));
    expect(fake.heldVersions.length).toBe(1);
    await app.advance();
    expect(app.status).toBe("task #2 done; z archives it");
    // The version answer arrives after the write: the poll lets the
    // write's own reload stand rather than note it as from elsewhere.
    fake.heldVersions[0]!();
    await polled;
    expect(app.status).toBe("task #2 done; z archives it");
    const t = app.rows.find((r) => key(rowTarget(r)) === "task:2")?.task?.task;
    expect(t?.status).toBe("done");
  });

  it("do not undo a move made while the server was answering", async () => {
    const { app, fake } = await open();
    selectKey(app, "task:1");
    fake.hold = true;
    fake.version = 2;
    const polled = app.poll();
    await new Promise((r) => setTimeout(r, 0));
    app.move(1);
    expect(selected(app)).toBe("subtask:1");
    fake.held[0]!();
    await polled;
    expect(selected(app)).toBe("subtask:1");
    expect(app.status).toBe("changed elsewhere, reloaded");
  });

  it("are not applied by a poll when a question opened while it was answering", async () => {
    const { app, fake } = await open();
    fake.hold = true;
    fake.version = 2;
    fake.data.projects[0]!.tasks = [];
    const polled = app.poll();
    await new Promise((r) => setTimeout(r, 0));
    selectKey(app, "task:4");
    app.requestDelete();
    fake.held[0]!();
    await polled;
    expect(app.rows.map((r) => key(rowTarget(r)))).toContain("task:4");
    expect(selected(app)).toBe("task:4");
  });

  it("keep a poll's error until a poll that looks goes well", async () => {
    const { app, fake } = await open();
    fake.down = true;
    await app.poll();
    expect(app.error).toBe("Failed to fetch");
    // A question opened without a key, as the test can; a key would
    // clear the line itself. The poll that skips it must not.
    app.modal = { kind: "confirmDelete", row: app.rows[0]! };
    await app.poll();
    expect(app.error).toBe("Failed to fetch");
    await app.confirm(false);
    fake.down = false;
    await app.poll();
    expect(app.error).toBe("");
  });

  it("are not polled for while a form or question is open", async () => {
    const { app, fake } = await open();
    selectKey(app, "task:1");
    app.requestDelete();
    fake.version = 2;
    fake.data.projects[0]!.tasks = [];
    await app.poll();
    expect(app.rows.map((r) => key(rowTarget(r)))).toContain("task:4");
    await app.confirm(false);
    await app.poll();
    expect(app.rows.map((r) => key(rowTarget(r)))).not.toContain("task:4");
  });
});
