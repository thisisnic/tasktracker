<script lang="ts">
  import { untrack } from "svelte";
  import { message, type AppState, type Modal } from "../lib/app.svelte";
  import FormShell, { autofocus } from "./FormShell.svelte";

  // Adds or edits an area: a name and the area it sits in. The area
  // field is only shown once there is an area to pick; when editing,
  // the area itself and everything inside it are left out.
  let { app, modal: opened }: { app: AppState; modal: Extract<Modal, { kind: "area" }> } = $props();
  // The form is built from the modal it was opened with; App keys the
  // form on it, so a new one is a new form.
  const modal = untrack(() => opened);

  let name = $state(modal.existing?.name ?? "");
  let parent = $state(modal.parentId);
  let error = $state("");
  let saving = $state(false);

  const title = $derived(modal.existing ? `Edit area #${modal.existing.id}` : "New area");
  const options = $derived(app.areas.filter((a) => !modal.blocked.has(a.id)));

  async function save() {
    if (name.trim() === "") {
      error = "say what the area is";
      return;
    }
    saving = true;
    try {
      await app.saveArea(modal, name, options.length > 0 ? parent : 0);
    } catch (e) {
      error = message(e);
    } finally {
      saving = false;
    }
  }
</script>

<FormShell {title} {error} {saving} onsave={save} oncancel={() => app.cancelForm()}>
  <label>
    Area
    <span class="hint">a name to group projects under</span>
    <input type="text" bind:value={name} {@attach autofocus} />
  </label>
  {#if options.length > 0}
    <label>
      Inside
      <select bind:value={parent}>
        <option value={0}>(top level)</option>
        {#each options as a (a.id)}
          <option value={a.id}>{app.areaPath(a.id)}</option>
        {/each}
      </select>
    </label>
  {/if}
</FormShell>
