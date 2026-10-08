<script lang="ts">
  import { tick } from "svelte";
  import { Chip, IconButton } from "@kenn-io/kit-ui";
  import { ChevronDown, ChevronRight, FileText } from "@lucide/svelte";
  import { areaOpenTasks, isOpen, openTasks, ticked } from "../lib/api";
  import type { AppState } from "../lib/app.svelte";
  import { bucketName, overdue, rowTarget, type Row } from "../lib/rows";

  // The list: areas and projects as headings that fold, tasks under
  // them with what is due, subtasks under those; a task and a subtask
  // are each a checkbox, ticked when done. A click selects a row and
  // opens it, through onrow: its form, or the detail for a heading;
  // enter opens the detail. The checkbox and the fold chevron act
  // without opening anything.
  let { app, onrow }: { app: AppState; onrow: () => void } = $props();

  let listEl = $state<HTMLElement>();

  // The selected row is kept in view as the keys move it.
  $effect(() => {
    const i = app.cursor;
    void tick().then(() => {
      listEl?.querySelector<HTMLElement>(`[data-index="${i}"]`)?.scrollIntoView({ block: "nearest" });
    });
  });

  function finished(r: Row): boolean {
    if (r.kind === "project") return r.project!.project.state !== "active";
    if (r.kind === "task") return !isOpen(r.task!.task);
    return false;
  }

  function foldable(r: Row): boolean {
    return r.kind === "area" || r.kind === "project" || r.kind === "heading";
  }

  function indent(r: Row): number {
    if (app.view === "deadline") return r.kind === "subtask" ? 1 : 0;
    switch (r.kind) {
      case "task":
        return r.depth + 1;
      case "subtask":
        return r.depth + 2;
      default:
        return r.depth;
    }
  }

  function openCount(r: Row): string {
    const n = r.kind === "area" ? areaOpenTasks(r.area!) : openTasks(r.project!);
    return n === 0 ? "" : n === 1 ? "1 open" : `${n} open`;
  }

  function onFold(e: MouseEvent, i: number) {
    e.stopPropagation();
    app.select(i);
    app.toggleFold();
  }

  // The box shows the row as the data has it, never a click that was
  // dropped or refused: the click is stopped from flipping it, and the
  // write's reload flips it when the write lands. A task's box ticks
  // it done and unticks it back to todo; a dropped task's is clear,
  // since dropped is not done, and a click ticks it done. The box never
  // takes focus, so the keys keep working after a click on it.
  function onTick(e: MouseEvent, i: number) {
    e.preventDefault();
    e.stopPropagation();
    app.select(i);
    void app.tick();
  }

  // A click in the list puts focus on the list itself, not on the row
  // or the box clicked, and not left on whatever control was clicked
  // before: the window takes the keys from the list, while a focused
  // switch or button would take them for itself.
  function takeFocus(e: MouseEvent) {
    e.preventDefault();
    listEl?.focus();
  }

  function onRow(i: number) {
    app.select(i);
    onrow();
  }
</script>

{#if app.rows.length === 0}
  <div class="empty">
    {#if app.view === "deadline" && !app.showAll}
      <p>No open tasks listed.</p>
      <p class="hint">Open tasks in finished projects are hidden; Show archived lists them.</p>
    {:else if app.view === "deadline"}
      <p>No open tasks.</p>
      <p class="hint">By project lists finished tasks too.</p>
    {:else if !app.showAll}
      <p>No projects yet.</p>
      <p class="hint">Add one with New project; Show archived lists finished projects.</p>
    {:else}
      <p>No projects yet.</p>
      <p class="hint">Add one with New project.</p>
    {/if}
  </div>
{:else}
  <div class="rows" role="listbox" aria-label="Rows" tabindex="-1" bind:this={listEl}>
    {#each app.rows as r, i (`${r.kind}:${rowTarget(r).id}`)}
      <!-- The keys are taken by the window and act on the selection,
           never on a row that was clicked earlier: a click selects and
           opens its form, and focus goes to the list, so nothing a row
           could answer to with a key handler would be right. -->
      <!-- svelte-ignore a11y_click_events_have_key_events -->
      <div
        class="row {r.kind}"
        class:selected={i === app.cursor}
        class:finished={finished(r)}
        role="option"
        aria-selected={i === app.cursor}
        tabindex="-1"
        data-index={i}
        style:--indent={indent(r)}
        onmousedown={takeFocus}
        onclick={() => onRow(i)}
      >
        {#if foldable(r)}
          <span class="lead">
            <IconButton size="sm" ariaLabel={app.isFolded(rowTarget(r)) ? "Unfold" : "Fold"} onclick={(e) => onFold(e, i)}>
              {#if app.isFolded(rowTarget(r))}<ChevronRight size={16} />{:else}<ChevronDown size={16} />{/if}
            </IconButton>
          </span>
        {:else}
          <span class="lead">
            <input
              type="checkbox"
              class="tick"
              checked={r.kind === "task" ? r.task!.task.status === "done" : r.subtask!.done}
              aria-label="Done"
              tabindex="-1"
              onmousedown={takeFocus}
              onclick={(e) => onTick(e, i)}
            />
          </span>
        {/if}

        <span class="main">
          {#if r.kind === "heading"}
            <span class="name">{bucketName(r.bucket!)}</span>
          {:else if r.kind === "area" || r.kind === "project"}
            <span class="name">{r.kind === "area" ? r.area!.area.name : r.project!.project.name}</span>
          {:else if r.kind === "task"}
            <span class="title">{r.task!.task.title}</span>
            {#if app.view === "deadline"}<span class="where">{r.project!.project.name}</span>{/if}
          {:else}
            <span class="title">{r.subtask!.title}</span>
          {/if}
        </span>

        <span class="side">
          {#if r.kind === "heading"}
            <span class="count">{r.count === 1 ? "1 task" : `${r.count} tasks`}</span>
          {:else if r.kind === "area"}
            <span class="count">{openCount(r)}</span>
          {:else if r.kind === "project"}
            {#if r.project!.project.state !== "active"}
              <Chip size="sm" tone="muted">{r.project!.project.state}</Chip>
            {:else}
              <span class="count">{openCount(r)}</span>
            {/if}
          {:else if r.kind === "task"}
            {@const t = r.task!}
            {#if t.task.notes}<span class="icon" title="has notes"><FileText size={14} /></span>{/if}
            {#if t.subtasks.length > 0}<span class="count">{ticked(t)}/{t.subtasks.length}</span>{/if}
            {#if t.task.status === "dropped"}<Chip size="sm" tone="muted">dropped</Chip>{/if}
            {#if t.task.archived}<Chip size="sm" tone="muted">archived</Chip>{/if}
            {#if t.task.due}
              <Chip size="sm" tone={overdue(t.task, app.today) ? "danger" : isOpen(t.task) ? "neutral" : "muted"}>{t.task.due}</Chip>
            {/if}
          {/if}
        </span>
      </div>
    {/each}
  </div>
{/if}

<style>
  .empty {
    padding: var(--space-6) var(--space-5);
    color: var(--text-secondary);
  }

  .empty p {
    margin: 0 0 var(--space-2);
  }

  .hint {
    color: var(--text-muted);
    font-size: 0.9em;
  }

  .rows {
    max-width: 960px;
    margin: 0 auto;
    padding: var(--space-3) var(--space-4) var(--space-6);
    outline: none;
  }

  .row {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    min-height: 36px;
    padding: var(--space-1) var(--space-3);
    padding-left: calc(var(--space-3) + var(--indent, 0) * 24px);
    border-radius: var(--radius-md);
    border-left: 3px solid transparent;
    cursor: pointer;
  }

  .row:hover {
    background: var(--bg-surface-hover);
  }

  .row.selected {
    background: var(--bg-surface-hover);
    border-left-color: var(--accent-blue);
  }

  .row.area,
  .row.heading {
    margin-top: var(--space-3);
  }

  .row.area .name,
  .row.heading .name {
    font-weight: var(--font-weight-semibold);
  }

  .row.heading .name {
    text-transform: var(--label-transform, uppercase);
    letter-spacing: var(--letter-spacing-label, 0.04em);
    font-size: 0.85em;
    color: var(--text-secondary);
  }

  .row.project .name {
    font-weight: var(--font-weight-medium);
  }

  .row.finished .name,
  .row.finished .title {
    color: var(--text-muted);
  }

  .row.task.finished .title {
    text-decoration: line-through;
  }

  .lead {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    flex-shrink: 0;
  }

  .main {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .where {
    color: var(--text-muted);
    margin-left: var(--space-2);
    font-size: 0.9em;
  }

  .side {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    flex-shrink: 0;
    color: var(--text-muted);
    font-size: 0.9em;
  }

  .icon {
    display: inline-flex;
    color: var(--text-muted);
  }

  .tick {
    width: 16px;
    height: 16px;
    margin: 0;
    accent-color: var(--accent-blue);
    cursor: pointer;
  }

</style>
