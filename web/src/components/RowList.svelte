<script lang="ts">
  import { tick } from "svelte";
  import { areaOpenTasks, isOpen, openTasks, ticked } from "../lib/api";
  import type { AppState } from "../lib/app.svelte";
  import { bucketName, overdue, rowTarget, type Row } from "../lib/rows";

  let { app }: { app: AppState } = $props();

  let listEl = $state<HTMLElement>();

  // The selected row is kept in view as the keys move it, as the
  // terminal UI scrolls its pane.
  $effect(() => {
    const i = app.cursor;
    void tick().then(() => {
      listEl?.querySelector<HTMLElement>(`[data-index="${i}"]`)?.scrollIntoView({ block: "nearest" });
    });
  });

  function glyph(r: Row): string {
    switch (r.task!.task.status) {
      case "doing":
        return "◐";
      case "done":
        return "●";
      case "dropped":
        return "⊘";
      default:
        return "○";
    }
  }

  function finished(r: Row): boolean {
    if (r.kind === "project") return r.project!.project.state !== "active";
    if (r.kind === "task") return !isOpen(r.task!.task);
    return false;
  }

  /** The text at the right edge: counts, state, due date. */
  function right(r: Row): string[] {
    switch (r.kind) {
      case "heading":
        return [r.count === 1 ? "1 task" : `${r.count} tasks`];
      case "area": {
        const n = areaOpenTasks(r.area!);
        return n > 0 ? [`${n} open`] : [];
      }
      case "project": {
        const p = r.project!;
        if (p.project.state !== "active") return [p.project.state];
        const n = openTasks(p);
        return n > 0 ? [`${n} open`] : [];
      }
      case "task": {
        const t = r.task!;
        const parts: string[] = [];
        if (t.subtasks.length > 0) parts.push(`${ticked(t)}/${t.subtasks.length}`);
        if (t.task.notes) parts.push("≡");
        if (t.task.archived) parts.push("archived");
        return parts;
      }
      default:
        return [];
    }
  }

  function foldable(r: Row): boolean {
    return r.kind === "area" || r.kind === "project" || r.kind === "heading";
  }

  function onFold(e: MouseEvent, i: number) {
    e.stopPropagation();
    app.select(i);
    app.toggleFold();
  }

  function onRowClick(i: number) {
    app.select(i);
  }

  function onRowDoubleClick(i: number) {
    app.select(i);
    app.edit();
  }

  // A row focused by a click answers Enter and space by making sure it
  // is the selection; the key then reaches the window, which steps the
  // selection on, as it does from anywhere else on the page.
  function onRowKey(e: KeyboardEvent, i: number) {
    if (e.key === "Enter" || e.key === " ") app.select(i);
  }
</script>

{#if app.rows.length === 0}
  <div class="empty">
    {#if app.view === "deadline"}
      no open tasks<br /><span class="hint">v goes back to by project</span>
    {:else if !app.showAll}
      no projects yet<br /><span class="hint">A adds one; f shows finished projects</span>
    {:else}
      no projects yet<br /><span class="hint">A adds one</span>
    {/if}
  </div>
{:else}
  <div class="rows" role="listbox" aria-label="Rows" bind:this={listEl}>
    {#each app.rows as r, i (`${r.kind}:${rowTarget(r).id}`)}
      {@const deadline = app.view === "deadline"}
      {@const indent = deadline ? (r.kind === "subtask" ? 1 : 0) : r.depth + (r.kind === "task" ? 1 : r.kind === "subtask" ? 2 : 0)}
      <div
        class="row {r.kind}"
        class:selected={i === app.cursor}
        class:finished={finished(r)}
        role="option"
        aria-selected={i === app.cursor}
        tabindex="-1"
        data-index={i}
        style:--indent={indent}
        onclick={() => onRowClick(i)}
        ondblclick={() => onRowDoubleClick(i)}
        onkeydown={(e) => onRowKey(e, i)}
      >
        <span class="left">
          {#if foldable(r)}
            <button
              class="fold"
              type="button"
              aria-label={app.isFolded(rowTarget(r)) ? "unfold" : "fold"}
              onclick={(e) => onFold(e, i)}>{app.isFolded(rowTarget(r)) ? "▸" : "▾"}</button
            >
          {/if}
          {#if r.kind === "heading"}
            {bucketName(r.bucket!)}
          {:else if r.kind === "area"}
            {r.area!.area.name}
          {:else if r.kind === "project"}
            {r.project!.project.name}
          {:else if r.kind === "task"}
            <span class="glyph {r.task!.task.status}">{glyph(r)}</span>
            <span class="title">{r.task!.task.title}</span>
            {#if deadline}<span class="project-name">{r.project!.project.name}</span>{/if}
          {:else}
            <span class="box">[{r.subtask!.done ? "x" : " "}]</span>
            <span class="title">{r.subtask!.title}</span>
          {/if}
        </span>
        <span class="right">
          {#each right(r) as part (part)}
            <span class="part">{part}</span>
          {/each}
          {#if r.kind === "task" && r.task!.task.due}
            <span class="part due" class:overdue={overdue(r.task!.task, app.today)}>{r.task!.task.due}</span>
          {/if}
        </span>
      </div>
    {/each}
  </div>
{/if}

<style>
  .empty {
    padding: var(--space-4);
    color: var(--text-muted);
  }

  .hint {
    font-size: 0.9em;
  }

  .rows {
    padding: var(--space-2) 0;
    font-family: var(--font-mono);
    font-size: 0.95em;
  }

  .row {
    display: flex;
    justify-content: space-between;
    gap: var(--space-3);
    padding: 2px var(--space-3) 2px calc(var(--space-3) + var(--indent, 0) * 1.25em);
    cursor: default;
    white-space: nowrap;
  }

  .row:hover {
    background: var(--bg-surface-hover);
  }

  .row.selected {
    background: var(--accent-blue);
    color: #fff;
  }

  .row.selected .part,
  .row.selected .project-name,
  .row.selected .glyph,
  .row.selected .due {
    color: inherit;
  }

  .row.area {
    color: var(--accent-purple);
    font-weight: var(--font-weight-semibold);
  }

  .row.heading {
    color: var(--accent-blue);
    font-weight: var(--font-weight-semibold);
  }

  .row.project {
    color: var(--accent-teal);
    font-weight: var(--font-weight-semibold);
  }

  .row.finished {
    color: var(--text-muted);
  }

  .left {
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }

  .right {
    flex-shrink: 0;
    color: var(--text-secondary);
    display: inline-flex;
    gap: var(--space-3);
  }

  .fold {
    all: unset;
    cursor: pointer;
    display: inline-block;
    width: 1.1em;
  }

  .glyph.doing {
    color: var(--accent-amber);
  }

  .project-name {
    color: var(--text-muted);
    margin-left: var(--space-2);
  }

  .due.overdue {
    color: var(--accent-red);
  }
</style>
