<script lang="ts">
  import { Button, Modal } from "@kenn-io/kit-ui";

  // A yes/no question before something is hidden or deleted. y answers
  // yes, as in the terminal UI; Escape, n and the Cancel button answer no.
  let { question, onanswer }: { question: string; onanswer: (yes: boolean) => void } = $props();

  function onkeydown(e: KeyboardEvent) {
    if (e.key === "y" || e.key === "Y") {
      e.preventDefault();
      onanswer(true);
    } else if (e.key === "n" || e.key === "N") {
      e.preventDefault();
      onanswer(false);
    }
  }
</script>

<svelte:window {onkeydown} />

<Modal title="Are you sure?" tone="danger" onclose={() => onanswer(false)}>
  <p class="question">{question}</p>
  {#snippet footer()}
    <Button type="button" onclick={() => onanswer(false)}>No</Button>
    <Button type="button" surface="solid" tone="danger" onclick={() => onanswer(true)}>Yes</Button>
  {/snippet}
</Modal>

<style>
  .question {
    margin: 0;
    overflow-wrap: anywhere;
  }
</style>
