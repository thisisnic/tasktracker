<script lang="ts">
  import { untrack } from "svelte";
  import { message, type AppState, type Modal } from "../lib/app.svelte";
  import FormShell, { autofocus } from "./FormShell.svelte";

  let { app, modal: opened }: { app: AppState; modal: Extract<Modal, { kind: "subtask" }> } = $props();
  // The form is built from the modal it was opened with; App keys the
  // form on it, so a new one is a new form.
  const modal = untrack(() => opened);

  let title = $state(modal.existing?.title ?? "");
  let error = $state("");
  let saving = $state(false);

  const heading = $derived(
    modal.existing ? `Edit subtask #${modal.existing.id}` : `New subtask under #${modal.under.id} ${modal.under.title}`,
  );

  async function save() {
    if (title.trim() === "") {
      error = "say what the subtask is";
      return;
    }
    saving = true;
    try {
      await app.saveSubtask(modal, title);
    } catch (e) {
      error = message(e);
    } finally {
      saving = false;
    }
  }
</script>

<FormShell title={heading} {error} {saving} onsave={save} oncancel={() => app.cancelForm()}>
  <label>
    Subtask
    <input type="text" bind:value={title} {@attach autofocus} />
  </label>
</FormShell>
