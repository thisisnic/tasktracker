// The page's state and every action on it, with the terminal UI's rules
// for where the selection lands and what the status line says.

import {
  api as realApi,
  areaPath,
  type AreaEdit,
  type Client,
  flatAreas,
  inside,
  isOpen,
  nextState,
  nextStatus,
  type Area,
  type Goals,
  type Outline,
  type Project,
  type ProjectEdit,
  type State,
  type Status,
  type Subtask,
  type Task,
  type TaskEdit,
  type TaskNode,
} from "./api";
import {
  bucketName,
  bucketOf,
  containers,
  deadlineContainer,
  deadlineRows,
  eachProject,
  eachTask,
  headingOf,
  indexOf,
  rowTarget,
  sameTarget,
  sooner,
  targetKey,
  targetLabel,
  todayStr,
  treeRows,
  type Row,
  type Target,
  type View,
} from "./rows";

/** What is open over the two panes: a form, or a question. */
export type Modal =
  | { kind: "area"; existing: Area | null; parentId: number; blocked: Set<number> }
  | { kind: "project"; existing: Project | null; areaId: number }
  | { kind: "task"; existing: Task | null; projectId: number; copyFrom: TaskNode | null }
  | { kind: "subtask"; existing: Subtask | null; under: Task }
  | { kind: "confirmDelete"; row: Row }
  | { kind: "confirmState"; row: Row };

/** What a heading says to actions that act on a task, project or area. */
export const headingHint = "headings group tasks by how soon they are due; ←/→ folds one, e and d act on tasks";

const FOLDS_KEY = "tasktracker-folds";
const POLL_MS = 2000;

export class AppState {
  private api: Client;
  /** The version of the binary serving the page, for the top bar. */
  version = $state("");
  goals = $state<Goals>({ readable: false, goals: [] });
  outline = $state<Outline>({ areas: [], projects: [] });
  /** Every area, flat, for paths and pick lists. */
  areas = $derived(flatAreas(this.outline));
  view = $state<View>("project");
  showAll = $state(false);
  today = $state("");
  /** The folded areas, projects and headings, by target key, each with
   * the stamp of the row's creation. SQLite gives a deleted row's id to
   * the next row made, so a kept fold is matched by stamp as well as
   * id; a heading has no stamp. */
  folds = $state<Record<string, string>>({});
  rows = $derived.by(() => {
    const folded = new Set(Object.keys(this.folds));
    return this.view === "deadline" ? deadlineRows(this.outline, folded, this.today) : treeRows(this.outline, folded);
  });
  cursor = $state(0);
  modal = $state<Modal | null>(null);
  /** Whether the selected row's detail is open in the drawer. */
  drawer = $state(false);
  /** The row the drawer is showing, for telling a move from a loss. */
  private shown: Target | null = null;
  status = $state("");
  error = $state("");
  /** error came from a poll, so a poll that goes well clears it. */
  private pollError = false;
  ready = $state(false);
  fatal = $state("");

  private loaded = "";
  private loadedOnce = false;
  /** A reload took in a change from elsewhere that the status line
   * has not yet said; say() and the poll say it after the action's
   * own message, as the terminal UI does. */
  private unnoted = false;
  /** Counts reloads, so one that started earlier cannot put its older
   * answer over a newer one's. */
  private loads = 0;
  /** How many reloads the page started itself, after a write of its
   * own or on a key, are in flight. The poll waits for them: the
   * version moves as soon as the server handles a write, and a poll's
   * reload then would both overtake the page's own, leaving its caller
   * to land on stale rows, and note the write as a change from
   * elsewhere. Actions wait too, so no form opens on rows about to be
   * replaced. */
  private busy = $state(0);
  private pollTimer: ReturnType<typeof setTimeout> | null = null;

  /** The clock the date is read from; tests pin it. */
  private now: () => Date;

  constructor(api: Client = realApi, now: () => Date = () => new Date()) {
    this.api = api;
    this.now = now;
    this.today = todayStr(now());
  }

  // ---- loading ----

  /** Loads the page. A first load that fails, as when the browser opens
   * its home page before the server is listening, is tried again at the
   * poll interval until it goes well; the page shows the error meanwhile
   * and clears it once it loads. */
  async start(): Promise<void> {
    if (this.pollTimer) clearTimeout(this.pollTimer);
    try {
      this.loadFolds();
      this.version = (await this.api.meta()).version;
      await this.refresh();
      this.fatal = "";
      this.ready = true;
      this.schedulePoll();
    } catch (e) {
      this.fatal = message(e);
      this.pollTimer = setTimeout(() => void this.start(), POLL_MS);
    }
  }

  /** Reloads the outline, keeping the selection on the same row where it
   * still exists: keep, or the row selected as the answer arrives, so a
   * move made while the server was answering is not undone. The
   * version comes with the outline, so a change that lands after it is
   * loaded again by the next poll rather than missed. A version other
   * than the last one loaded means something wrote in between, and the
   * status line says so after the action's own message. The version
   * moves on the page's own writes too, so after one of them, own, the
   * server's write count at its end is left out of the comparison: the
   * rest is the server's epoch and SQLite's data version, which moves
   * when another process commits and not when this server does.
   * A write from another tab in that moment is taken in without the
   * note, then; the next poll's reload notes the one after it. A
   * reload overtaken by a later one drops its answer, which is older
   * than what the later one has shown; so does a poll's, which is not
   * mine, when a form or a write has started meanwhile, since the form
   * should not have the rows move under it and the write's own reload
   * will show what it did. A reload that is mine is one busy holds for,
   * so nothing starts under it, and its answer always lands. */
  async reload(keep?: Target | null, own = false, mine = own): Promise<void> {
    const seq = ++this.loads;
    const resp = await this.api.outline(this.showAll);
    if (seq !== this.loads || (!mine && !this.idle())) return;
    if (keep === undefined) keep = this.selectedTarget();
    if (this.loadedOnce && (own ? dataPart(resp.version) !== dataPart(this.loaded) : resp.version !== this.loaded)) {
      this.unnoted = true;
    }
    this.today = todayStr(this.now());
    this.outline = resp.outline;
    this.pruneFolds();
    this.loaded = resp.version;
    this.loadedOnce = true;
    if (keep && !this.selectNear(keep) && this.view === "deadline") {
      // The row is gone from the list, as when a task is finished here.
      // The cursor keeps its place; if the heading of the next bucket
      // slid into it, prefer the next task, or the one before, so the
      // hand stays on tasks rather than on whatever moved up.
      this.clampCursor();
      if (this.selected()?.kind === "heading" && !this.stepToTask(1)) this.stepToTask(-1);
    }
    this.clampCursor();
    this.settleDrawer();
  }

  // ---- the drawer ----

  /** Opens the selected row's detail. */
  openDrawer(): void {
    if (!this.selected()) return;
    this.drawer = true;
    this.shown = this.selectedTarget();
  }

  /** Enter: opens the selected row's detail, or closes it when it is
   * open, so the keyboard reaches what the drawer shows. Space steps
   * the row on, as it does in the terminal UI, where enter does the
   * same; here enter opens, as it does in any list on the web. */
  toggleDrawer(): void {
    if (this.drawer) this.closeDrawer();
    else this.openDrawer();
  }

  closeDrawer(): void {
    this.drawer = false;
    this.shown = null;
  }

  /** Keeps the drawer in step with the rows: it follows the selection
   * as the keys move it, and closes once the row it showed is no longer
   * listed, whether a write of the page's own, a change from elsewhere
   * or a fold took it out, so it never slides on to whichever row the
   * selection landed on next. Called wherever the rows or the selection
   * change. */
  private settleDrawer(): void {
    if (!this.drawer) return;
    if (this.shown && indexOf(this.rows, this.shown) < 0) {
      this.closeDrawer();
      return;
    }
    this.shown = this.selectedTarget();
  }

  /** The reload behind r: everything is read again, goals included,
   * since another process may have changed any of it. */
  async refresh(): Promise<void> {
    this.goals = await this.api.goals();
    await this.reload(undefined, false, true);
  }

  /** Whether the page is browsing with nothing in flight: no form or
   * question open, and no write of its own under way. */
  private idle(): boolean {
    return this.modal === null && this.busy === 0;
  }

  /** Whether a write of the page's own, or a reload it asked for, is
   * in flight, for controls to be disabled meanwhile. */
  get inFlight(): boolean {
    return this.busy > 0;
  }

  /** Whether an action may start. While the page's own write and the
   * reload after it are in flight, the rows are about to change, and an
   * action read off them would act on what they were: a second press
   * of space would send the same status again. The terminal UI writes
   * inside the keypress and never sees this; here the key is dropped
   * and the next one works on the new rows. */
  private settled(): boolean {
    return this.busy === 0;
  }

  /** Moves the page's date on when midnight has passed, so a page left
   * open overnight buckets by deadline from the new day, as the terminal
   * UI reads the clock each time it draws. The rows follow the date, so
   * the selection is found again where it went. */
  turnOfDay(): void {
    const today = todayStr(this.now());
    if (today === this.today) return;
    const keep = this.selectedTarget();
    this.today = today;
    if (keep) this.selectNear(keep);
    this.clampCursor();
    this.settleDrawer();
  }

  /** Sets the status line to the action's message, then notes a change
   * taken in from elsewhere after it, once: a hint that says where a
   * row went is still wanted when an agent on the CLI is writing every
   * few seconds. */
  private say(status: string): void {
    this.status = status;
    this.noteChangedElsewhere();
  }

  private noteChangedElsewhere(): void {
    if (!this.unnoted) return;
    this.unnoted = false;
    const note = "changed elsewhere, reloaded";
    if (this.status === "") this.status = note;
    else if (!this.status.endsWith(note)) this.status += "; " + note;
  }

  private schedulePoll(): void {
    if (this.pollTimer) clearTimeout(this.pollTimer);
    this.pollTimer = setTimeout(() => void this.poll(), POLL_MS);
  }

  /** Reloads the rows when another process has changed the database
   * since they were loaded. It only looks while browsing and while no
   * write of the page's own is in flight: a form holds what was typed,
   * a question names the selected row, so neither should have the rows
   * move under it, and a write lands where its own reload puts it. A
   * poll's own error is cleared by the next poll that goes well; an
   * error from the last action is left for the next action to clear,
   * and a poll's error never takes its place. */
  async poll(): Promise<void> {
    try {
      // The page is looked at again after each wait: a key pressed
      // while the server was answering may have opened a form or
      // started a write, and then the answer is not acted on.
      if (!this.idle()) return;
      this.turnOfDay();
      const { version } = await this.api.version();
      if (!this.idle()) return;
      if (version !== this.loaded) {
        const goals = await this.api.goals();
        if (!this.idle()) return;
        this.goals = goals;
        await this.reload();
        this.noteChangedElsewhere();
      }
      // Only a poll that looked and found the server well clears the
      // error of one that did not.
      if (this.pollError) {
        this.error = "";
        this.pollError = false;
      }
    } catch (e) {
      if (this.error === "" || this.pollError) {
        this.error = message(e);
        this.pollError = true;
      }
    } finally {
      this.schedulePoll();
    }
  }

  // ---- folds ----

  private loadFolds(): void {
    try {
      const raw = localStorage.getItem(FOLDS_KEY);
      const parsed: unknown = raw ? JSON.parse(raw) : {};
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
        const folds: Record<string, string> = {};
        for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) if (typeof v === "string") folds[k] = v;
        this.folds = folds;
      }
    } catch {
      // Storage that cannot be read gives a session with nothing folded.
    }
  }

  private saveFolds(): void {
    try {
      localStorage.setItem(FOLDS_KEY, JSON.stringify(this.folds));
    } catch {
      // Storage that cannot be written loses the folds at the next
      // load; the page still folds for this session.
    }
  }

  /** Forgets folds on areas and projects whose id now belongs to a row
   * made at another time, so a deleted row's fold cannot land on
   * whatever next reuses its id. A fold whose row is not listed is kept:
   * a finished project hidden until f still exists, and keeps its fold
   * for when it shows again. */
  private pruneFolds(): void {
    const present = new Map<string, string>();
    for (const a of this.areas) present.set(targetKey({ kind: "area", id: a.id }), a.created_at);
    eachProject(this.outline, (p) => {
      present.set(targetKey({ kind: "project", id: p.project.id }), p.project.created_at);
    });
    let pruned = false;
    const folds = { ...this.folds };
    for (const [k, made] of Object.entries(folds)) {
      const now = present.get(k);
      if (now !== undefined && now !== made) {
        delete folds[k];
        pruned = true;
      }
    }
    if (pruned) {
      this.folds = folds;
      this.saveFolds();
    }
  }

  isFolded(t: Target): boolean {
    return targetKey(t) in this.folds;
  }

  private fold(t: Target, made: string): void {
    this.folds = { ...this.folds, [targetKey(t)]: made };
    this.saveFolds();
  }

  private unfold(t: Target): void {
    const folds = { ...this.folds };
    delete folds[targetKey(t)];
    this.folds = folds;
    this.saveFolds();
  }

  /** ← or →: folds the selected area, project or heading, or the one
   * the selected row is in, and unfolds it again. */
  toggleFold(): void {
    const r = this.selected();
    if (!r) return;
    this.clearMessages();
    let t: Target;
    let made = "";
    let name: string;
    let held: string;
    let empty = "";
    if (r.kind === "area") {
      t = rowTarget(r);
      made = r.area!.area.created_at;
      name = r.area!.area.name;
      held = "what is in it";
      if (r.area!.areas.length + r.area!.projects.length === 0) empty = "the area is empty";
    } else if (r.kind === "heading") {
      t = rowTarget(r);
      name = bucketName(r.bucket!);
      held = "its tasks";
    } else if (this.view === "deadline") {
      t = headingOf(r, this.today);
      name = bucketName(bucketOf(r.task!.task.due, this.today));
      held = "its tasks";
    } else {
      t = { kind: "project", id: r.project!.project.id };
      made = r.project!.project.created_at;
      name = r.project!.project.name;
      held = "its tasks";
      if (r.project!.tasks.length === 0) empty = "the project has no tasks listed";
    }
    if (this.isFolded(t) && sameTarget(rowTarget(r), t)) {
      // Unfold before asking whether there is anything to hide, so a
      // fold whose contents have since gone can still be undone.
      this.unfold(t);
      this.status = "expanded " + name;
    } else if (empty !== "") {
      this.status = "nothing to hide: " + empty;
      return;
    } else {
      this.fold(t, made);
      this.status = "collapsed " + name + "; ← shows " + held + " again";
    }
    this.selectTarget(t);
    this.settleDrawer();
  }

  /** Selects t, first expanding every fold around it, so a row folded
   * away can still be shown. By deadline that is the heading the task
   * is under; the tree's folds are left as they are. */
  reveal(t: Target): void {
    for (const c of this.around(t)) if (this.isFolded(c)) this.unfold(c);
    this.selectTarget(t);
    this.settleDrawer();
  }

  /** What t is inside in the current view, outermost first. */
  private around(t: Target): Target[] {
    if (this.view === "deadline") {
      const h = deadlineContainer(this.outline, t, this.today);
      return h ? [h] : [];
    }
    return containers(this.outline, t);
  }

  // ---- selection ----

  selected(): Row | null {
    return this.rows[this.cursor] ?? null;
  }

  selectedTarget(): Target | null {
    const r = this.selected();
    return r ? rowTarget(r) : null;
  }

  private clampCursor(): void {
    if (this.cursor >= this.rows.length) this.cursor = Math.max(0, this.rows.length - 1);
  }

  /** Moves the cursor to the next task row (dir 1) or the previous one
   * (dir -1) and reports whether there was one. */
  private stepToTask(dir: 1 | -1): boolean {
    for (let i = this.cursor + dir; i >= 0 && i < this.rows.length; i += dir) {
      if (this.rows[i]!.kind === "task") {
        this.cursor = i;
        return true;
      }
    }
    return false;
  }

  /** Moves the cursor to the row for t and reports whether it found one.
   * When t is gone, for instance because it was just hidden, the cursor
   * stays where it is. */
  selectTarget(t: Target): boolean {
    const i = indexOf(this.rows, t);
    if (i < 0) return false;
    this.cursor = i;
    return true;
  }

  /** Moves the cursor to the row for t, or when that row is folded away,
   * to the nearest container that is listed, innermost first. By
   * deadline that is the task's heading. A row that is gone altogether
   * has no containers, and the cursor stays put. */
  selectNear(t: Target): boolean {
    if (this.selectTarget(t)) return true;
    const cs = this.around(t);
    for (let i = cs.length - 1; i >= 0; i--) if (this.selectTarget(cs[i]!)) return true;
    return false;
  }

  select(i: number): void {
    if (i >= 0 && i < this.rows.length) this.cursor = i;
    this.settleDrawer();
  }

  move(by: number): void {
    this.clearMessages();
    this.cursor = Math.min(Math.max(0, this.cursor + by), Math.max(0, this.rows.length - 1));
    this.settleDrawer();
  }

  first(): void {
    this.clearMessages();
    this.cursor = 0;
    this.settleDrawer();
  }

  last(): void {
    this.clearMessages();
    this.cursor = Math.max(0, this.rows.length - 1);
    this.settleDrawer();
  }

  /** The area a new area or project goes in by default: the selected
   * row's area, or the top when nothing is selected or the selection is
   * a heading, which is in no area. */
  areaHere(): number {
    const r = this.selected();
    if (!r || r.kind === "heading") return 0;
    return r.kind === "area" ? r.area!.area.id : (r.project!.project.area_id ?? 0);
  }

  areaPath(id: number): string {
    return areaPath(this.areas, id);
  }

  private clearMessages(): void {
    this.status = "";
    this.error = "";
    this.pollError = false;
  }

  // ---- views ----

  /** f: shows archived tasks and finished projects, or hides them. A
   * reload that fails leaves the toggle as it was, with the rows. */
  async toggleShowAll(): Promise<void> {
    if (!this.settled()) return;
    this.clearMessages();
    this.busy++;
    try {
      await this.toggleShowAllNow();
    } finally {
      this.busy--;
    }
  }

  private async toggleShowAllNow(): Promise<void> {
    this.showAll = !this.showAll;
    let status = this.showAll ? "showing archived tasks and finished projects" : "hiding archived tasks and finished projects";
    if (this.view === "deadline") {
      // Finished tasks are never listed here, so what f changes is
      // whether open tasks in finished projects are.
      status += this.showAll
        ? "; here that adds open tasks in finished projects"
        : "; here that drops open tasks in finished projects";
    }
    if (await this.tryReload()) this.say(status);
    else this.showAll = !this.showAll;
  }

  /** v: switches between by project and by deadline. The switch changes
   * no data, so it is done in one step, with the landing, and nothing
   * can be pressed between the two: the rows follow the view at once,
   * and the row selected before is found again where it went. The poll
   * keeps the rows fresh as before. */
  toggleView(): void {
    if (!this.settled()) return;
    this.clearMessages();
    const from = this.selected();
    const keep = this.selectedTarget();
    if (this.view === "project") {
      this.view = "deadline";
      this.status = "by deadline: open tasks under overdue, next 7 days, next 30 days, longer, no deadline";
    } else {
      this.view = "project";
      this.status = "by project";
    }
    if (keep) this.selectNear(keep);
    this.clampCursor();
    if (from) {
      if (this.view === "deadline") this.landByDeadline(from);
      else this.landByProject(from);
    }
    this.settleDrawer();
  }

  /** Moves the cursor after switching to by deadline from a row that
   * has no row there: an area, a project, or a finished task or
   * subtask. It goes to the soonest due task listed from the same area
   * or project; when none is listed, to the heading that hides the first
   * such task; and to the top when there is no such task at all. From an
   * open task or subtask the cursor is already on the same row. */
  private landByDeadline(from: Row): void {
    const within = new Set<number>();
    switch (from.kind) {
      case "task":
      case "subtask":
        if (deadlineContainer(this.outline, rowTarget(from), this.today)) return;
        within.add(from.project!.project.id);
        break;
      case "project":
        within.add(from.project!.project.id);
        break;
      case "area":
        eachProject(from.area!, (p) => {
          within.add(p.project.id);
        });
        break;
      default:
        return;
    }
    const i = this.rows.findIndex((r) => r.kind === "task" && within.has(r.project!.project.id));
    if (i >= 0) {
      this.cursor = i;
      return;
    }
    // Nothing listed: every such task is under a folded heading. Land on
    // the heading of the soonest due one, as the listed case would.
    let soonest: Task | null = null;
    eachTask(this.outline, (p, tk) => {
      if (isOpen(tk.task) && within.has(p.project.id) && (!soonest || sooner(tk.task, soonest))) soonest = tk.task;
    });
    if (!soonest || !this.selectNear({ kind: "task", id: (soonest as Task).id })) this.cursor = 0;
  }

  /** Moves the cursor after switching to by project from a heading,
   * which has no row there: to the first task listed that is due about
   * as soon, or to the top when none is. */
  private landByProject(from: Row): void {
    if (from.kind !== "heading") return;
    let first: Task | null = null;
    eachTask(this.outline, (_, tk) => {
      if (isOpen(tk.task) && bucketOf(tk.task.due, this.today) === from.bucket) {
        first = tk.task;
        return false;
      }
    });
    if (!first || !this.selectNear({ kind: "task", id: (first as Task).id })) this.cursor = 0;
  }

  // ---- actions ----

  /** Runs a reload and reports whether it went well; its error goes on
   * the status line. own says the reload follows the page's own write. */
  private async tryReload(keep?: Target | null, own = false): Promise<boolean> {
    try {
      await this.reload(keep, own, true);
      return true;
    } catch (e) {
      this.error = message(e);
      return false;
    }
  }

  /** r: reads everything again. */
  async refreshNow(): Promise<void> {
    if (!this.settled()) return;
    this.clearMessages();
    this.busy++;
    try {
      await this.refresh();
      this.say("reloaded");
    } catch (e) {
      this.error = message(e);
    } finally {
      this.busy--;
    }
  }

  /** Runs a write, then reloads. The status is set by the caller before
   * or after; the error of either step goes on the status line. A write
   * that failed may still have landed, as when the server could not
   * read the row back, so the rows are reloaded either way. */
  private async write(fn: () => Promise<unknown>): Promise<boolean> {
    this.busy++;
    try {
      try {
        await fn();
      } catch (e) {
        this.error = message(e);
        try {
          await this.reload(undefined, true);
        } catch {
          // The write's error is the one to show.
        }
        return false;
      }
      return await this.tryReload(undefined, true);
    } finally {
      this.busy--;
    }
  }

  /** space: a task steps todo, doing, done; a subtask toggles its tick;
   * a project steps active, done, shelved, asking first before leaving
   * active. An area has no state to step. */
  async advance(): Promise<void> {
    if (!this.settled()) return;
    const r = this.selected();
    if (!r) return;
    this.clearMessages();
    switch (r.kind) {
      case "area":
        this.status = "areas have no state; e renames, d deletes";
        return;
      case "heading":
        this.status = headingHint;
        return;
      case "subtask": {
        const s = r.subtask!;
        const done = !s.done;
        const status = `${done ? "ticked" : "unticked"} subtask #${s.id}`;
        if (await this.write(() => this.api.tickSubtask(s.id, done))) this.say(status);
        return;
      }
      case "task": {
        const t = r.task!.task;
        const next = nextStatus(t.status);
        let status = `task #${t.id} ${next}`;
        if (t.archived) {
          // An archived task shown with f: stepping it back to todo
          // reopens it, which takes it out of the archive.
          status += " (back from the archive)";
        } else if (next === "done") {
          status += this.finishedHint();
        }
        if (await this.write(() => this.api.editTask(t.id, { status: next }))) this.say(status);
        return;
      }
      case "project": {
        const p = r.project!.project;
        if (nextState(p.state) !== "active") {
          // A done or shelved project is hidden with everything in it,
          // which is too much to do on one stray keypress.
          this.modal = { kind: "confirmState", row: r };
          return;
        }
        await this.setProjectState(p.id, "active");
      }
    }
  }

  private async setProjectState(id: number, next: Project["state"]): Promise<void> {
    let status = `project #${id} ${next}`;
    if (next !== "active") status += this.hiddenHint("finished");
    if (await this.write(() => this.api.editProject(id, { state: next }))) this.say(status);
  }

  /** x: a task is dropped, a project shelved. Pressing it again on a
   * dropped task or a shelved project brings it back. */
  async drop(): Promise<void> {
    if (!this.settled()) return;
    const r = this.selected();
    if (!r) return;
    this.clearMessages();
    switch (r.kind) {
      case "task": {
        const t = r.task!.task;
        const st = t.status === "dropped" ? "todo" : "dropped";
        let status = `task #${t.id} ${st}`;
        if (t.archived && st === "dropped") {
          // An archived done task shown with f: it stays archived.
          status += " (still archived)";
        } else if (t.archived) {
          // An archived dropped task shown with f: reopening it takes
          // it out of the archive.
          status += " (back from the archive)";
        } else if (st === "dropped") {
          status += this.finishedHint();
        }
        if (await this.write(() => this.api.editTask(t.id, { status: st }))) this.say(status);
        return;
      }
      case "project": {
        const p = r.project!.project;
        const st = p.state === "shelved" ? "active" : "shelved";
        const status = `project #${p.id} ${st}` + this.hiddenHint("finished");
        if (await this.write(() => this.api.editProject(p.id, { state: st }))) this.say(status);
        return;
      }
      case "area":
        this.status = "areas have no state; d deletes one and moves what is in it up a level";
        return;
      case "heading":
        this.status = headingHint;
        return;
      default:
        this.status = "subtasks are ticked with space, or deleted with d";
    }
  }

  /** z: a finished task is put away, out of the tree, and an archived
   * task is brought back. An open task cannot be archived, since it is
   * still work to do. */
  async archive(): Promise<void> {
    if (!this.settled()) return;
    const r = this.selected();
    if (!r) return;
    this.clearMessages();
    switch (r.kind) {
      case "area":
      case "project":
        this.status = "only tasks are archived; space or x on a project finishes it, which hides it";
        return;
      case "heading":
        this.status = headingHint;
        return;
      case "subtask":
        this.status = "subtasks go with their task; z on the task archives it";
        return;
    }
    const t = r.task!.task;
    if (t.archived) {
      if (await this.write(() => this.api.archiveTask(t.id, false))) this.say(`task #${t.id} back from the archive`);
    } else if (isOpen(t)) {
      // A finished task leaves the by-deadline list.
      const where = this.view === "deadline" ? " in by project" : "";
      this.status = `task #${t.id} is still ${t.status}; space finishes it or x drops it, then z${where} archives it`;
    } else {
      const status = `task #${t.id} archived` + this.hiddenHint("archived");
      if (await this.write(() => this.api.archiveTask(t.id, true))) this.say(status);
    }
  }

  /** Follows a task being done or dropped: it stays listed until
   * archived. */
  private finishedHint(): string {
    if (this.view === "deadline") return " (gone from this list; by project shows it until archived)";
    return "; z archives it";
  }

  /** Explains where something just put out of sight went: what is
   * "archived" for a task or "finished" for a project. */
  private hiddenHint(what: string): string {
    return this.showAll ? "" : ` (hidden; f shows ${what})`;
  }

  /** d: asks before deleting the selected row. */
  requestDelete(): void {
    if (!this.settled()) return;
    const r = this.selected();
    if (!r) return;
    this.clearMessages();
    if (r.kind === "heading") {
      this.status = headingHint;
      return;
    }
    this.modal = { kind: "confirmDelete", row: r };
  }

  /** The question the delete confirmation asks. */
  deleteQuestion(r: Row): string {
    switch (r.kind) {
      case "area":
        return `delete area #${r.area!.area.id} “${r.area!.area.name}” and move what is in it up a level?`;
      case "project":
        return `delete project #${r.project!.project.id} “${r.project!.project.name}” and every task in it?`;
      case "task":
        return `delete task #${r.task!.task.id} “${r.task!.task.title}” and its subtasks?`;
      default:
        return `delete subtask #${r.subtask!.id} “${r.subtask!.title}”?`;
    }
  }

  /** The question the state confirmation asks: marking a project done
   * or shelving it hides it with every task in it. */
  stateQuestion(r: Row): string {
    const p = r.project!.project;
    const verb = nextState(p.state) === "shelved" ? "shelve" : "mark done";
    return `${verb} project #${p.id} “${p.name}” and hide it with every task in it?`;
  }

  /** Answers the open question. */
  async confirm(yes: boolean): Promise<void> {
    if (!this.settled()) return;
    const m = this.modal;
    this.modal = null;
    if (!m || (m.kind !== "confirmDelete" && m.kind !== "confirmState")) return;
    if (!yes) {
      this.status = "kept";
      return;
    }
    const r = m.row;
    if (m.kind === "confirmState") {
      await this.setProjectState(r.project!.project.id, nextState(r.project!.project.state));
      return;
    }
    const t = rowTarget(r);
    const del = () => {
      switch (r.kind) {
        case "area":
          return this.api.deleteArea(t.id);
        case "project":
          return this.api.deleteProject(t.id);
        case "task":
          return this.api.deleteTask(t.id);
        default:
          return this.api.deleteSubtask(t.id);
      }
    };
    // The drawer closes before the write, so it does not show the row
    // being deleted meanwhile, and opens again if the delete did not
    // happen and the row is still there to show: one deleted elsewhere
    // in the meantime is gone from the rows the refusal reloads, and
    // the drawer must not open on whichever row the selection landed on.
    const shown = this.drawer ? this.shown : null;
    this.closeDrawer();
    if (await this.write(del)) this.say(`deleted ${targetLabel(t)}`);
    else if (shown && sameTarget(this.selectedTarget(), shown)) this.openDrawer();
  }

  // ---- forms ----

  /** n: a new area, inside the selected row's area. */
  newArea(): void {
    if (!this.settled()) return;
    this.clearMessages();
    this.modal = { kind: "area", existing: null, parentId: this.areaHere(), blocked: new Set() };
  }

  /** A: a new project, in the selected row's area. */
  newProject(): void {
    if (!this.settled()) return;
    this.clearMessages();
    this.modal = { kind: "project", existing: null, areaId: this.areaHere() };
  }

  /** a: a new task under the selected row's project. */
  newTask(): void {
    if (!this.settled()) return;
    const r = this.selected();
    this.clearMessages();
    if (!r) {
      this.status =
        this.view === "deadline"
          ? "no task to add under; v goes back to by project, where a adds one under a project"
          : "add a project first: press A";
      return;
    }
    if (r.kind === "area") {
      this.status = "select a project to add a task under; A adds one here";
      return;
    }
    if (r.kind === "heading") {
      this.status = "select a task to add one under its project";
      return;
    }
    this.modal = { kind: "task", existing: null, projectId: r.project!.project.id, copyFrom: null };
  }

  /** s: a new subtask under the selected task. */
  newSubtask(): void {
    if (!this.settled()) return;
    const r = this.selected();
    this.clearMessages();
    if (!r || r.kind === "area" || r.kind === "project" || r.kind === "heading") {
      this.status = "select a task to add a subtask under";
      return;
    }
    this.modal = { kind: "subtask", existing: null, under: r.task!.task };
  }

  /** c: a new task copied from the selected one. */
  copyTask(): void {
    if (!this.settled()) return;
    const r = this.selected();
    this.clearMessages();
    if (!r || r.kind === "area" || r.kind === "project" || r.kind === "heading") {
      this.status = "select a task to copy";
      return;
    }
    this.modal = { kind: "task", existing: null, projectId: r.task!.task.project_id, copyFrom: r.task! };
  }

  /** e: edits the selected row in its form. */
  edit(): void {
    if (!this.settled()) return;
    const r = this.selected();
    if (!r) return;
    this.clearMessages();
    switch (r.kind) {
      case "heading":
        this.status = headingHint;
        return;
      case "area": {
        const a = r.area!.area;
        this.modal = { kind: "area", existing: a, parentId: a.parent_id ?? 0, blocked: inside(r.area!) };
        return;
      }
      case "project": {
        const p = r.project!.project;
        this.modal = { kind: "project", existing: p, areaId: p.area_id ?? 0 };
        return;
      }
      case "task": {
        const t = r.task!.task;
        this.modal = { kind: "task", existing: t, projectId: t.project_id, copyFrom: null };
        return;
      }
      case "subtask":
        this.modal = { kind: "subtask", existing: r.subtask!, under: r.task!.task };
    }
  }

  cancelForm(): void {
    this.modal = null;
    this.status = "cancelled";
  }

  /** What a form does once its write went through: the rows are read
   * again, the saved row is shown even if it was folded away, and the
   * status line says what was saved. Areas and projects have no row by
   * deadline, so the status says where it went. */
  async saved(t: Target, status: string): Promise<void> {
    this.modal = null;
    // The reload may land before reveal unfolds the way to a row that
    // was saved into a fold, and close the drawer as if the row had
    // gone; it is opened again once the row is shown.
    const shown = this.drawer ? this.shown : null;
    this.busy++;
    try {
      if (!(await this.tryReload(undefined, true))) return;
    } finally {
      this.busy--;
    }
    this.reveal(t);
    if (shown && !this.drawer && sameTarget(this.selectedTarget(), t)) this.openDrawer();
    if (this.view === "deadline" && (t.kind === "area" || t.kind === "project")) status += " (v shows it by project)";
    this.say(status);
  }

  // ---- saving ----
  //
  // What each form does on Save, with the fields as typed. An edit
  // sends only the fields that changed, so a rename or a move made
  // elsewhere while the form was open is not written back, and a
  // stored issue the server would refuse does not block changes to
  // the other fields. Each throws what the server refused, for the
  // form to show with what was typed.

  /** Saves the area form. */
  async saveArea(m: Extract<Modal, { kind: "area" }>, name: string, parentId: number): Promise<void> {
    let id: number;
    if (m.existing) {
      const patch: AreaEdit = {};
      if (name !== m.existing.name) patch.name = name;
      if (parentId !== (m.existing.parent_id ?? 0)) patch.parent_id = parentId;
      if (Object.keys(patch).length > 0) await this.api.editArea(m.existing.id, patch);
      id = m.existing.id;
    } else {
      id = (await this.api.addArea(name, parentId)).id;
    }
    await this.saved({ kind: "area", id }, `saved area #${id}`);
  }

  /** Saves the project form. goals is the set the form holds, in any
   * order. */
  async saveProject(
    m: Extract<Modal, { kind: "project" }>,
    f: { name: string; description: string; areaId: number; goals: number[]; state: State },
  ): Promise<void> {
    let id: number;
    if (m.existing) {
      const p = m.existing;
      const patch: ProjectEdit = {};
      if (f.name !== p.name) patch.name = f.name;
      if (f.description !== (p.description ?? "")) patch.description = f.description;
      if (f.areaId !== (p.area_id ?? 0)) patch.area_id = f.areaId;
      if (!sameIds(f.goals, p.goal_ids ?? [])) patch.goal_ids = f.goals;
      if (f.state !== p.state) patch.state = f.state;
      if (Object.keys(patch).length > 0) await this.api.editProject(p.id, patch);
      id = p.id;
    } else {
      id = (await this.api.addProject(f.name, f.description, f.areaId, f.goals)).id;
    }
    await this.saved({ kind: "project", id }, `saved project #${id}`);
  }

  /** What the task form starts with: the task being edited, or for a
   * copy the original's title, due date, notes and project, with the
   * issue blank, since the original's issue is the original's work. A
   * new task, copied or not, starts as todo. */
  taskFields(m: Extract<Modal, { kind: "task" }>): TaskFields {
    const from = m.existing ?? m.copyFrom?.task ?? null;
    return {
      title: from?.title ?? "",
      due: from?.due ?? "",
      issue: m.existing?.issue ?? "",
      notes: from?.notes ?? "",
      status: m.existing?.status ?? "todo",
      project: m.projectId,
    };
  }

  /** Saves the task form: an edit, a copy, or a new task. */
  async saveTask(m: Extract<Modal, { kind: "task" }>, f: TaskFields): Promise<void> {
    if (m.existing) {
      const t = m.existing;
      const patch: TaskEdit = {};
      if (f.title !== t.title) patch.title = f.title;
      if (f.due !== (t.due ?? "")) patch.due = f.due;
      if (f.issue !== (t.issue ?? "")) patch.issue = f.issue;
      if (f.notes !== (t.notes ?? "")) patch.notes = f.notes;
      if (f.status !== t.status) patch.status = f.status;
      if (f.project !== t.project_id) patch.project_id = f.project;
      if (Object.keys(patch).length > 0) await this.api.editTask(t.id, patch);
      await this.saved({ kind: "task", id: t.id }, `saved task #${t.id}`);
    } else if (m.copyFrom) {
      const n = await this.api.copyTask(m.copyFrom.task.id, {
        title: f.title,
        due: f.due,
        issue: f.issue,
        notes: f.notes,
        project_id: f.project,
      });
      // The status line reports what was copied, not what the row
      // showed when c was pressed.
      let status = `copied task #${m.copyFrom.task.id} to task #${n.task.id}`;
      if (n.subtasks.length === 1) status += " with its subtask, unticked";
      else if (n.subtasks.length > 1) status += ` with its ${n.subtasks.length} subtasks, unticked`;
      await this.saved({ kind: "task", id: n.task.id }, status);
    } else {
      const t = await this.api.addTask(f.project, f.title, f.due, f.issue, f.notes);
      await this.saved({ kind: "task", id: t.id }, `saved task #${t.id}`);
    }
  }

  /** Every project, finished ones included, for the task form's project
   * field: a task can be moved anywhere. */
  projects(): Promise<Project[]> {
    return this.api.projects();
  }

  /** Saves the subtask form. */
  async saveSubtask(m: Extract<Modal, { kind: "subtask" }>, title: string): Promise<void> {
    const s = m.existing ? await this.api.renameSubtask(m.existing.id, title) : await this.api.addSubtask(m.under.id, title);
    await this.saved({ kind: "subtask", id: s.id }, `saved subtask #${s.id}`);
  }

  /** The help line: the keys the page answers to, named for the view. */
  helpLine(): string {
    const keys =
      "n area · A project · a task · s subtask · c copy task · e edit · enter detail · space next status/tick · x drop · z archive · d delete · f show archived";
    if (this.view === "deadline") return keys + " · v by project · ←/→ fold/unfold · j/k move";
    return keys + " · v by deadline · ←/→ fold/unfold · j/k move";
  }
}

/** The task form's fields. */
export interface TaskFields {
  title: string;
  due: string;
  issue: string;
  notes: string;
  status: Status;
  project: number;
}

/** Whether two goal sets hold the same ids, in any order. */
function sameIds(a: number[], b: number[]): boolean {
  const sa = [...new Set(a)].sort((x, y) => x - y);
  const sb = [...new Set(b)].sort((x, y) => x - y);
  return sa.length === sb.length && sa.every((v, i) => v === sb[i]);
}

/** The state of the database in a version the server reports: the
 * server's epoch and SQLite's data version, leaving off the server's
 * own write count after the last dot. */
function dataPart(version: string): string {
  const i = version.lastIndexOf(".");
  return i < 0 ? version : version.slice(0, i);
}

export function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}
