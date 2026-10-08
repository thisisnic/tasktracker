<script module lang="ts">
  /** Focuses the field it is attached to when the form opens. */
  export function autofocus(el: HTMLElement): void {
    el.focus();
  }
</script>

<script lang="ts">
  import type { Snippet } from "svelte";
  import { Button, Modal } from "@kenn-io/kit-ui";

  // The frame every form shares: a titled dialog, the fields, the error
  // of the last save kept under them with what was typed, and Save and
  // Cancel. Enter in a field saves; Escape cancels, as in the terminal
  // UI's forms. While a save is in flight nothing cancels: the write
  // has been sent and will land, so "cancelled" would be untrue, and a
  // form opened in its place would be closed by the save.
  let {
    title,
    error,
    saving,
    onsave,
    oncancel,
    children,
  }: {
    title: string;
    error: string;
    saving: boolean;
    onsave: () => void;
    oncancel: () => void;
    children: Snippet;
  } = $props();

  function onsubmit(e: SubmitEvent) {
    e.preventDefault();
    if (!saving) onsave();
  }

  function cancel() {
    if (!saving) oncancel();
  }
</script>

<Modal {title} onclose={cancel} closable={!saving} closeOnOverlayClick={!saving} maxWidth="min(560px, calc(100vw - 32px))">
  <form class="form" {onsubmit}>
    {@render children()}
    {#if error}
      <p class="error" role="alert">{error}</p>
    {/if}
    <div class="buttons">
      <Button type="submit" surface="solid" tone="info" disabled={saving}>Save</Button>
      <Button type="button" onclick={cancel} disabled={saving}>Cancel</Button>
    </div>
  </form>
</Modal>

<style>
  .form {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
    min-width: min(480px, calc(100vw - 64px));
  }

  .error {
    margin: 0;
    color: var(--accent-red);
  }

  .buttons {
    display: flex;
    gap: var(--space-2);
    justify-content: flex-end;
    margin-top: var(--space-2);
  }

  :global(.form label) {
    display: flex;
    flex-direction: column;
    gap: var(--space-1);
    font-size: 0.9em;
    color: var(--text-secondary);
  }

  :global(.form .hint) {
    color: var(--text-muted);
    font-size: 0.85em;
  }

  :global(.form input[type="text"]),
  :global(.form textarea),
  :global(.form select) {
    font: inherit;
    color: var(--text-primary);
    background: var(--bg-surface);
    border: var(--border-width) solid var(--border-default);
    border-radius: var(--radius-sm);
    padding: var(--space-2);
  }

  :global(.form input[type="text"]:focus),
  :global(.form textarea:focus),
  :global(.form select:focus) {
    outline: var(--focus-ring);
  }
</style>
