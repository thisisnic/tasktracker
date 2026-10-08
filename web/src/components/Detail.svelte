<script lang="ts">
  import { Button, Chip } from "@kenn-io/kit-ui";
  import { CheckSquare, Square } from "@lucide/svelte";
  import { areaCounts, areaOpenTasks, isOpen, issueRef, nextState, nextStatus, openTasks, ticked, type Project } from "../lib/api";
  import type { AppState } from "../lib/app.svelte";
  import { bucketName, bucketSpan, daysUntil, dueWords, overdue, rowTarget } from "../lib/rows";

  // The selected row in full, with its actions, for the drawer. The
  // drawer's header carries the name; this is the rest.
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

  const statusTone = { todo: "neutral", done: "success", dropped: "muted" } as const;

  const name = $derived.by(() => {
    if (!row) return "";
    switch (row.kind) {
      case "heading":
        return bucketName(row.bucket!);
      case "area":
        return row.area!.area.name;
      case "project":
        return row.project!.project.name;
      case "task":
        return row.task!.task.title;
      default:
        return row.subtask!.title;
    }
  });
</script>

{#if row}
<div class="body">
  <h2>{name}</h2>
  {#if row.kind === "heading"}
    {@const b = row.bucket!}
    <dl>
      <dt>Due</dt>
      <dd>{bucketSpan(b, app.today)}</dd>
      <dt>Tasks</dt>
      <dd>{row.count} open</dd>
    </dl>
    {#if folded}<p class="muted">Folded; its tasks are hidden.</p>{/if}
    <div class="actions">
      <Button size="sm" onclick={() => app.toggleFold()}>{folded ? "Unfold" : "Fold"}</Button>
    </div>
  {:else if row.kind === "area"}
    {@const a = row.area!}
    {@const counts = areaCounts(a)}
    <dl>
      {#if a.area.parent_id}
        <dt>In</dt>
        <dd>{app.areaPath(a.area.parent_id)}</dd>
      {/if}
      <dt>Holds</dt>
      <dd>{counts.areas} areas, {counts.projects} projects</dd>
      <dt>Tasks</dt>
      <dd>{areaOpenTasks(a)} open</dd>
      <dt>Id</dt>
      <dd>#{a.area.id}</dd>
    </dl>
    {#if folded}<p class="muted">Folded; what is in it is hidden.</p>{/if}
    <div class="actions">
      <Button size="sm" surface="solid" tone="info" onclick={() => app.edit()}>Edit</Button>
      <Button size="sm" onclick={() => app.newProject()}>New project here</Button>
      <Button size="sm" onclick={() => app.newArea()}>New area here</Button>
      <Button size="sm" onclick={() => app.toggleFold()}>{folded ? "Unfold" : "Fold"}</Button>
      <span class="spacer"></span>
      <Button size="sm" tone="danger" onclick={() => app.requestDelete()}>Delete</Button>
    </div>
  {:else if row.kind === "project"}
    {@const p = row.project!}
    {@const goals = goalLabels(p.project)}
    <dl>
      {#if p.project.area_id}
        <dt>Area</dt>
        <dd>{app.areaPath(p.project.area_id)}</dd>
      {/if}
      <dt>State</dt>
      <dd><Chip size="sm" tone={p.project.state === "active" ? "info" : "muted"}>{p.project.state}</Chip></dd>
      <dt>Tasks</dt>
      <dd>{openTasks(p)} open, {p.tasks.length} listed</dd>
      <dt>Id</dt>
      <dd>#{p.project.id}</dd>
    </dl>
    {#if folded}<p class="muted">Folded; its tasks are hidden.</p>{/if}
    {#if p.project.description}
      <h3>About</h3>
      <p class="prose">{p.project.description}</p>
    {/if}
    {#if goals.length > 0}
      <h3>Goals</h3>
      <ul class="plain">
        {#each goals as g (g)}
          <li>{g}</li>
        {/each}
      </ul>
      {#if !app.goals.readable}<p class="muted">goaltracker's database could not be read, so the goals are shown by id.</p>{/if}
    {/if}
    <div class="actions">
      <Button size="sm" surface="solid" tone="info" onclick={() => app.edit()}>Edit</Button>
      <Button size="sm" onclick={() => app.newTask()}>New task</Button>
      <Button size="sm" onclick={() => void app.advance()}>Mark {nextState(p.project.state)}</Button>
      <Button size="sm" onclick={() => void app.drop()}>{p.project.state === "shelved" ? "Unshelve" : "Shelve"}</Button>
      <Button size="sm" onclick={() => app.toggleFold()}>{folded ? "Unfold" : "Fold"}</Button>
      <span class="spacer"></span>
      <Button size="sm" tone="danger" onclick={() => app.requestDelete()}>Delete</Button>
    </div>
  {:else if row.kind === "task"}
    {@const t = row.task!}
    {@const days = daysUntil(t.task.due, app.today)}
    {@const issue = issueRef(t.task.issue)}
    <dl>
      <dt>Project</dt>
      <dd>{row.project!.project.name}</dd>
      <dt>Status</dt>
      <dd>
        <Chip size="sm" tone={statusTone[t.task.status]}>{t.task.status}</Chip>
        {#if t.task.archived}<Chip size="sm" tone="muted">archived</Chip>{/if}
      </dd>
      {#if t.task.due}
        <dt>Due</dt>
        <dd class:overdue={overdue(t.task, app.today)}>
          {t.task.due}{#if days !== null && isOpen(t.task)}<span class="muted">{dueWords(days)}</span>{/if}
        </dd>
      {/if}
      {#if t.task.issue}
        <dt>Issue</dt>
        <dd>
          {#if issue}
            <a href={issue.link} target="_blank" rel="noopener noreferrer">{issue.ref}</a>
          {:else}
            {t.task.issue}
          {/if}
        </dd>
      {/if}
      <dt>Id</dt>
      <dd>#{t.task.id}</dd>
    </dl>
    {#if t.subtasks.length > 0}
      <h3>Subtasks <span class="muted">{ticked(t)}/{t.subtasks.length}</span></h3>
      <ul class="plain checks">
        {#each t.subtasks as s (s.id)}
          <li class:done={s.done}>
            {#if s.done}<CheckSquare size={16} />{:else}<Square size={16} />{/if}
            <span>{s.title}</span>
          </li>
        {/each}
      </ul>
    {/if}
    {#if t.task.notes}
      <h3>Notes</h3>
      <p class="prose">{t.task.notes}</p>
    {/if}
    <div class="actions">
      <Button size="sm" surface="solid" tone="info" onclick={() => app.edit()}>Edit</Button>
      <Button size="sm" onclick={() => void app.advance()}>Mark {nextStatus(t.task.status)}</Button>
      <Button size="sm" onclick={() => void app.drop()}>{t.task.status === "dropped" ? "Undrop" : "Drop"}</Button>
      <Button size="sm" onclick={() => void app.archive()}>{t.task.archived ? "Unarchive" : "Archive"}</Button>
      <Button size="sm" onclick={() => app.newSubtask()}>New subtask</Button>
      <Button size="sm" onclick={() => app.copyTask()}>Copy</Button>
      <span class="spacer"></span>
      <Button size="sm" tone="danger" onclick={() => app.requestDelete()}>Delete</Button>
    </div>
  {:else}
    {@const s = row.subtask!}
    <dl>
      <dt>Under</dt>
      <dd>{row.task!.task.title}</dd>
      <dt>Project</dt>
      <dd>{row.project!.project.name}</dd>
      <dt>Done</dt>
      <dd>{s.done ? "yes" : "not yet"}</dd>
      <dt>Id</dt>
      <dd>#{s.id}</dd>
    </dl>
    <div class="actions">
      <Button size="sm" surface="solid" tone="info" onclick={() => void app.advance()}>{s.done ? "Untick" : "Tick"}</Button>
      <Button size="sm" onclick={() => app.edit()}>Rename</Button>
      <span class="spacer"></span>
      <Button size="sm" tone="danger" onclick={() => app.requestDelete()}>Delete</Button>
    </div>
  {/if}
</div>
{/if}

<style>
  .body {
    padding: var(--space-5) var(--space-6) var(--space-6);
  }

  h2 {
    margin: 0 0 var(--space-5);
    font-size: 1.15em;
    font-weight: var(--font-weight-semibold);
    overflow-wrap: anywhere;
  }

  dl {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: var(--space-2) var(--space-4);
    margin: 0;
    align-items: baseline;
  }

  dt {
    color: var(--text-muted);
    font-size: 0.85em;
  }

  dd {
    margin: 0;
    overflow-wrap: anywhere;
    display: flex;
    gap: var(--space-2);
    align-items: center;
    flex-wrap: wrap;
  }

  h3 {
    margin: var(--space-5) 0 var(--space-2);
    font-size: 0.85em;
    color: var(--text-muted);
    font-weight: var(--font-weight-medium);
    text-transform: var(--label-transform, uppercase);
    letter-spacing: var(--letter-spacing-label, 0.04em);
  }

  .muted {
    color: var(--text-muted);
  }

  .overdue {
    color: var(--accent-red);
  }

  .prose {
    white-space: pre-wrap;
    margin: 0;
    overflow-wrap: anywhere;
    line-height: var(--line-height-prose, 1.5);
  }

  .plain {
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .checks li {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    padding: var(--space-1) 0;
  }

  .checks li.done {
    color: var(--text-muted);
  }

  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
    margin-top: var(--space-6);
    padding-top: var(--space-4);
    border-top: var(--border-width) solid var(--border-muted);
  }

  .spacer {
    flex: 1;
  }
</style>
