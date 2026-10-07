<script lang="ts">
  import { Button } from "@kenn-io/kit-ui";
  import { areaCounts, areaOpenTasks, isOpen, issueRef, nextState, nextStatus, openTasks, ticked, type Project } from "../lib/api";
  import type { AppState } from "../lib/app.svelte";
  import { bucketName, bucketSpan, daysUntil, dueWords, overdue, rowTarget } from "../lib/rows";

  let { app }: { app: AppState } = $props();

  const row = $derived(app.selected());
  const folded = $derived(row ? app.isFolded(rowTarget(row)) : false);

  /** The labelled goals for a project: "#3 run 500 km" when goaltracker
   * has the goal, "#3" when it does not or cannot be read. */
  function goalLabels(p: Project): string[] {
    const byId = new Map(app.goals.goals.map((g) => [g.id, g]));
    return (p.goal_ids ?? []).map((id) => {
      const g = byId.get(id);
      return g?.statement ? `#${id} ${g.statement}` : `#${id}`;
    });
  }

  function statusText(t: { status: string; archived?: boolean }): string {
    return t.archived ? `${t.status} · archived` : t.status;
  }
</script>

{#if row}
  {#if row.kind === "heading"}
    {@const b = row.bucket!}
    <h2>{bucketName(b)}</h2>
    <dl>
      <dt>due</dt>
      <dd>{bucketSpan(b, app.today)}</dd>
      <dt>tasks</dt>
      <dd>{row.count} open</dd>
    </dl>
    {#if folded}<p class="muted">collapsed; ← shows its tasks</p>{/if}
    <div class="actions">
      <Button size="sm" onclick={() => app.toggleFold()}>{folded ? "Unfold" : "Fold"}</Button>
    </div>
  {:else if row.kind === "area"}
    {@const a = row.area!}
    {@const counts = areaCounts(a)}
    <h2>{a.area.name}</h2>
    <dl>
      {#if a.area.parent_id}
        <dt>in</dt>
        <dd>{app.areaPath(a.area.parent_id)}</dd>
      {/if}
      <dt>holds</dt>
      <dd>{counts.areas} areas, {counts.projects} projects <span class="id">id #{a.area.id}</span></dd>
      <dt>tasks</dt>
      <dd>{areaOpenTasks(a)} open</dd>
    </dl>
    {#if folded}<p class="muted">collapsed; ← shows what is in it</p>{/if}
    <div class="actions">
      <Button size="sm" onclick={() => app.edit()}>Edit</Button>
      <Button size="sm" onclick={() => app.newProject()}>New project here</Button>
      <Button size="sm" onclick={() => app.newArea()}>New area here</Button>
      <Button size="sm" onclick={() => app.toggleFold()}>{folded ? "Unfold" : "Fold"}</Button>
      <Button size="sm" tone="danger" onclick={() => app.requestDelete()}>Delete</Button>
    </div>
  {:else if row.kind === "project"}
    {@const p = row.project!}
    {@const goals = goalLabels(p.project)}
    <h2>{p.project.name}</h2>
    <dl>
      {#if p.project.area_id}
        <dt>area</dt>
        <dd>{app.areaPath(p.project.area_id)}</dd>
      {/if}
      <dt>state</dt>
      <dd>{p.project.state} <span class="id">id #{p.project.id}</span></dd>
      <dt>tasks</dt>
      <dd>{openTasks(p)} open, {p.tasks.length} listed</dd>
    </dl>
    {#if folded}<p class="muted">collapsed; ← shows its tasks</p>{/if}
    {#if p.project.description}
      <h3>about</h3>
      <p class="prose">{p.project.description}</p>
    {/if}
    {#if goals.length > 0}
      <h3>goals</h3>
      <ul class="plain">
        {#each goals as g (g)}
          <li>{g}</li>
        {/each}
        {#if !app.goals.readable}<li class="muted">(goaltracker not readable)</li>{/if}
      </ul>
    {/if}
    <div class="actions">
      <Button size="sm" onclick={() => app.edit()}>Edit</Button>
      <Button size="sm" onclick={() => app.newTask()}>New task</Button>
      <Button size="sm" onclick={() => void app.advance()}>Mark {nextState(p.project.state)}</Button>
      <Button size="sm" onclick={() => void app.drop()}>{p.project.state === "shelved" ? "Unshelve" : "Shelve"}</Button>
      <Button size="sm" onclick={() => app.toggleFold()}>{folded ? "Unfold" : "Fold"}</Button>
      <Button size="sm" tone="danger" onclick={() => app.requestDelete()}>Delete</Button>
    </div>
  {:else if row.kind === "task"}
    {@const t = row.task!}
    {@const days = daysUntil(t.task.due, app.today)}
    {@const issue = issueRef(t.task.issue)}
    <h2>{t.task.title}</h2>
    <dl>
      <dt>project</dt>
      <dd>{row.project!.project.name}</dd>
      <dt>status</dt>
      <dd><span class={t.task.status}>{statusText(t.task)}</span> <span class="id">id #{t.task.id}</span></dd>
      {#if t.task.due}
        <dt>due</dt>
        <dd class:overdue={overdue(t.task, app.today)}>
          {t.task.due}{#if days !== null && isOpen(t.task)}&nbsp;&nbsp;{dueWords(days)}{/if}
        </dd>
      {/if}
      {#if t.task.issue}
        <dt>issue</dt>
        <dd>
          {#if issue}
            <a href={issue.link} target="_blank" rel="noopener noreferrer">{issue.ref}</a>
          {:else}
            {t.task.issue}
          {/if}
        </dd>
      {/if}
    </dl>
    {#if t.subtasks.length > 0}
      <h3>subtasks {ticked(t)}/{t.subtasks.length}</h3>
      <ul class="plain">
        {#each t.subtasks as s (s.id)}
          <li><span class="box">[{s.done ? "x" : " "}]</span> {s.title}</li>
        {/each}
      </ul>
    {/if}
    {#if t.task.notes}
      <h3>notes</h3>
      <p class="prose">{t.task.notes}</p>
    {/if}
    <div class="actions">
      <Button size="sm" onclick={() => app.edit()}>Edit</Button>
      <Button size="sm" onclick={() => void app.advance()}>Mark {nextStatus(t.task.status)}</Button>
      <Button size="sm" onclick={() => void app.drop()}>{t.task.status === "dropped" ? "Undrop" : "Drop"}</Button>
      <Button size="sm" onclick={() => void app.archive()}>{t.task.archived ? "Unarchive" : "Archive"}</Button>
      <Button size="sm" onclick={() => app.newSubtask()}>New subtask</Button>
      <Button size="sm" onclick={() => app.copyTask()}>Copy</Button>
      <Button size="sm" tone="danger" onclick={() => app.requestDelete()}>Delete</Button>
    </div>
  {:else}
    {@const s = row.subtask!}
    <h2>{s.title}</h2>
    <dl>
      <dt>under</dt>
      <dd>{row.task!.task.title}</dd>
      <dt>project</dt>
      <dd>{row.project!.project.name}</dd>
      <dt>done</dt>
      <dd>{s.done ? "ticked" : "not yet"} <span class="id">id #{s.id}</span></dd>
    </dl>
    <div class="actions">
      <Button size="sm" onclick={() => void app.advance()}>{s.done ? "Untick" : "Tick"}</Button>
      <Button size="sm" onclick={() => app.edit()}>Rename</Button>
      <Button size="sm" tone="danger" onclick={() => app.requestDelete()}>Delete</Button>
    </div>
  {/if}
{/if}

<style>
  h2 {
    margin: 0 0 var(--space-3);
    font-size: 1.1em;
    overflow-wrap: anywhere;
  }

  h3 {
    margin: var(--space-4) 0 var(--space-2);
    font-size: 0.85em;
    color: var(--accent-teal);
    font-weight: var(--font-weight-medium);
  }

  dl {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: var(--space-1) var(--space-3);
    margin: 0;
  }

  dt {
    color: var(--accent-teal);
  }

  dd {
    margin: 0;
    overflow-wrap: anywhere;
  }

  .id {
    color: var(--text-muted);
    margin-left: var(--space-3);
  }

  .muted {
    color: var(--text-muted);
  }

  .overdue {
    color: var(--accent-red);
  }

  .done {
    color: var(--accent-green);
  }

  .doing {
    color: var(--accent-amber);
  }

  .dropped {
    color: var(--text-muted);
  }

  .prose {
    white-space: pre-wrap;
    margin: 0;
    overflow-wrap: anywhere;
  }

  .plain {
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .box {
    font-family: var(--font-mono);
  }

  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
    margin-top: var(--space-5);
  }
</style>
