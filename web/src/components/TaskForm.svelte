<script lang="ts">
  import { untrack } from "svelte";
  import { onMount } from "svelte";
  import type { Project, Status } from "../lib/api";
  import { message, type AppState, type Modal } from "../lib/app.svelte";
  import FormShell, { autofocus } from "./FormShell.svelte";

  // Adds, edits or copies a task. The status field is only shown when
  // editing: a new task, copied or not, starts as todo. Every project is
  // offered for the project field, finished ones included, so a task
  // can be moved anywhere.
  let { app, modal: opened }: { app: AppState; modal: Extract<Modal, { kind: "task" }> } = $props();
  // The form is built from the modal it was opened with; App keys the
  // form on it, so a new one is a new form.
  const modal = untrack(() => opened);

  const start = untrack(() => app.taskFields(modal));
  let title = $state(start.title);
  let due = $state(start.due);
  let issue = $state(start.issue);
  let notes = $state(start.notes);
  let status = $state<Status>(start.status);
  let project = $state(start.project);
  let projects = $state<Project[]>([]);
  let error = $state("");
  let saving = $state(false);

  onMount(async () => {
    try {
      projects = await app.projects();
    } catch (e) {
      error = message(e);
    }
  });

  const heading = $derived.by(() => {
    if (modal.existing) return `Edit task #${modal.existing.id}`;
    if (!modal.copyFrom) return "New task";
    const n = modal.copyFrom.subtasks.length;
    let s = `New task copied from #${modal.copyFrom.task.id}`;
    if (n === 1) s += " (with its subtask)";
    else if (n > 1) s += ` (with its ${n} subtasks)`;
    return s;
  });

  function projectLabel(p: Project): string {
    return p.state === "active" ? p.name : `${p.name} (${p.state})`;
  }

  async function save() {
    if (title.trim() === "") {
      error = "say what the task is";
      return;
    }
    saving = true;
    try {
      await app.saveTask(modal, { title, due, issue, notes, status, project });
    } catch (e) {
      error = message(e);
    } finally {
      saving = false;
    }
  }
</script>

<FormShell title={heading} {error} {saving} onsave={save} oncancel={() => app.cancelForm()}>
  <label>
    Task
    <input type="text" bind:value={title} {@attach autofocus} />
  </label>
  <label>
    Due
    <span class="hint">YYYY-MM-DD, today, tomorrow, or blank</span>
    <input type="text" bind:value={due} />
  </label>
  <label>
    Issue
    <span class="hint">GitHub issue URL or owner/repo#N, or blank</span>
    <input type="text" bind:value={issue} />
  </label>
  <label>
    Notes
    <textarea rows="4" bind:value={notes}></textarea>
  </label>
  {#if modal.existing}
    <label>
      Status
      <select bind:value={status}>
        <option value="todo">todo</option>
        <option value="doing">doing</option>
        <option value="done">done</option>
        <option value="dropped">dropped</option>
      </select>
    </label>
  {/if}
  <label>
    Project
    <select bind:value={project}>
      {#each projects as p (p.id)}
        <option value={p.id}>{projectLabel(p)}</option>
      {/each}
    </select>
  </label>
</FormShell>
