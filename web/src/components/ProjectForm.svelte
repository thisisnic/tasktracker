<script lang="ts">
  import { untrack } from "svelte";
  import type { State } from "../lib/api";
  import { message, type AppState, type Modal } from "../lib/app.svelte";
  import FormShell, { autofocus } from "./FormShell.svelte";

  // Adds or edits a project. Goals come from goaltracker as a pick list
  // when its database can be read, and as a typed list of ids
  // otherwise. The area field is only shown once there are areas, and
  // the state field only when editing: a new project starts active.
  let { app, modal: opened }: { app: AppState; modal: Extract<Modal, { kind: "project" }> } = $props();
  // The form is built from the modal it was opened with; App keys the
  // form on it, so a new one is a new form.
  const modal = untrack(() => opened);

  let name = $state(modal.existing?.name ?? "");
  let description = $state(modal.existing?.description ?? "");
  let area = $state(modal.areaId);
  let projectState = $state<State>(modal.existing?.state ?? "active");
  let picks = $state<number[]>([...(modal.existing?.goal_ids ?? [])]);
  let goalText = $state((modal.existing?.goal_ids ?? []).join(", "));
  let error = $state("");
  let saving = $state(false);

  const title = $derived(modal.existing ? `Edit project #${modal.existing.id}` : "New project");
  const pickList = $derived(app.goals.readable && app.goals.goals.length > 0);
  // Goals linked to ids that goaltracker no longer has would be lost on
  // save, so they are offered too.
  const goalOptions = $derived.by(() => {
    const known = new Set(app.goals.goals.map((g) => g.id));
    const opts = app.goals.goals.map((g) => ({ id: g.id, label: `${g.period ?? ""}  #${g.id} ${g.statement ?? ""}` }));
    for (const id of modal.existing?.goal_ids ?? []) if (!known.has(id)) opts.push({ id, label: `#${id} (not in goaltracker)` });
    return opts;
  });

  /** Reads a comma- or space-separated list of positive integers. */
  function parseIds(s: string): number[] {
    const out: number[] = [];
    for (const part of s.split(/[,\s]+/).filter(Boolean)) {
      const n = Number(part.replace(/^#/, ""));
      if (!Number.isInteger(n) || n <= 0) throw new Error(`“${part}” is not a goal id`);
      out.push(n);
    }
    return out;
  }

  function togglePick(id: number, on: boolean) {
    picks = on ? [...picks, id] : picks.filter((p) => p !== id);
  }

  async function save() {
    if (name.trim() === "") {
      error = "say what the project is";
      return;
    }
    let goals: number[];
    try {
      goals = pickList ? picks : parseIds(goalText);
    } catch (e) {
      error = message(e);
      return;
    }
    saving = true;
    try {
      await app.saveProject(modal, { name, description, areaId: app.areas.length > 0 ? area : 0, goals, state: projectState });
    } catch (e) {
      error = message(e);
    } finally {
      saving = false;
    }
  }
</script>

<FormShell {title} {error} {saving} onsave={save} oncancel={() => app.cancelForm()}>
  <label>
    Project
    <input type="text" bind:value={name} {@attach autofocus} />
  </label>
  <label>
    About
    <textarea rows="3" bind:value={description}></textarea>
  </label>
  {#if app.areas.length > 0}
    <label>
      Area
      <select bind:value={area}>
        <option value={0}>(none)</option>
        {#each app.areas as a (a.id)}
          <option value={a.id}>{app.areaPath(a.id)}</option>
        {/each}
      </select>
    </label>
  {/if}
  {#if modal.existing}
    <label>
      State
      <select bind:value={projectState}>
        <option value="active">active</option>
        <option value="done">done</option>
        <option value="shelved">shelved</option>
      </select>
    </label>
  {/if}
  {#if pickList}
    <div>
      <span class="hint">Goals: goaltracker goals this project serves</span>
      <div class="checks">
        {#each goalOptions as g (g.id)}
          <label>
            <input type="checkbox" checked={picks.includes(g.id)} onchange={(e) => togglePick(g.id, e.currentTarget.checked)} />
            {g.label}
          </label>
        {/each}
      </div>
    </div>
  {:else}
    <label>
      Goals
      <span class="hint">goaltracker goal ids, comma-separated; blank for none</span>
      <input type="text" bind:value={goalText} />
    </label>
  {/if}
</FormShell>
