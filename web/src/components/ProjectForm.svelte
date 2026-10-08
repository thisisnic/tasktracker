<script lang="ts">
  import { untrack } from "svelte";
  import type { State } from "../lib/api";
  import { message, type AppState, type Modal } from "../lib/app.svelte";
  import FormShell, { autofocus } from "./FormShell.svelte";

  // Adds or edits a project. The area field is only shown once there
  // are areas, and the state field only when editing: a new project
  // starts active.
  let { app, modal: opened }: { app: AppState; modal: Extract<Modal, { kind: "project" }> } = $props();
  // The form is built from the modal it was opened with; App keys the
  // form on it, so a new one is a new form.
  const modal = untrack(() => opened);

  let name = $state(modal.existing?.name ?? "");
  let description = $state(modal.existing?.description ?? "");
  let area = $state(modal.areaId);
  let projectState = $state<State>(modal.existing?.state ?? "active");
  let error = $state("");
  let saving = $state(false);

  const title = $derived(modal.existing ? `Edit project #${modal.existing.id}` : "New project");
  async function save() {
    if (name.trim() === "") {
      error = "say what the project is";
      return;
    }
    saving = true;
    try {
      await app.saveProject(modal, { name, description, areaId: app.areas.length > 0 ? area : 0, state: projectState });
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
</FormShell>
