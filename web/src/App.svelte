<script lang="ts">
  import { onMount } from "svelte";
  import { Button, Notice, SegmentedControl, StatusBar, ThemeToggle, Toggle, TopBar } from "@kenn-io/kit-ui";
  import { AppState } from "./lib/app.svelte";
  import type { View } from "./lib/rows";
  import RowList from "./components/RowList.svelte";
  import Detail from "./components/Detail.svelte";
  import AreaForm from "./components/AreaForm.svelte";
  import ProjectForm from "./components/ProjectForm.svelte";
  import TaskForm from "./components/TaskForm.svelte";
  import SubtaskForm from "./components/SubtaskForm.svelte";
  import Confirm from "./components/Confirm.svelte";

  const app = new AppState();

  onMount(() => {
    void app.start();
  });

  const views = [
    { value: "project", label: "By project" },
    { value: "deadline", label: "By deadline" },
  ];

  function setView(v: string) {
    if (v !== app.view) app.toggleView();
  }

  // The terminal UI's keys, so a hand that knows them need not reach
  // for the mouse. A form or a question takes its own keys; typing in a
  // field is never a command; and space or enter on a focused button,
  // link or control presses it, as it does on any page.
  function onkeydown(e: KeyboardEvent) {
    if (!app.ready || app.modal) return;
    // A control that took the key, such as the view control taking an
    // arrow, has used it.
    if (e.defaultPrevented) return;
    const target = e.target as HTMLElement | null;
    if (target && (target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.isContentEditable)) return;
    if (e.ctrlKey || e.metaKey || e.altKey) return;
    if ((e.key === " " || e.key === "Enter") && target?.closest("button, a, select, [role=switch], [role=checkbox], [role=radio]")) return;
    let handled = true;
    switch (e.key) {
      case "j":
      case "ArrowDown":
        app.move(1);
        break;
      case "k":
      case "ArrowUp":
        app.move(-1);
        break;
      case "g":
      case "Home":
        app.first();
        break;
      case "G":
      case "End":
        app.last();
        break;
      case "ArrowLeft":
      case "ArrowRight":
        app.toggleFold();
        break;
      case " ":
      case "Enter":
        void app.advance();
        break;
      case "x":
        void app.drop();
        break;
      case "z":
        void app.archive();
        break;
      case "d":
        app.requestDelete();
        break;
      case "n":
        app.newArea();
        break;
      case "A":
        app.newProject();
        break;
      case "a":
        app.newTask();
        break;
      case "s":
        app.newSubtask();
        break;
      case "c":
        app.copyTask();
        break;
      case "e":
        app.edit();
        break;
      case "f":
        void app.toggleShowAll();
        break;
      case "v":
        app.toggleView();
        break;
      case "r":
        void app.refreshNow();
        break;
      default:
        handled = false;
    }
    if (handled) e.preventDefault();
  }
</script>

<svelte:window {onkeydown} />

<TopBar ariaLabel="tasktracker">
  {#snippet left()}
    <span class="brand" title={app.version ? "tasktracker " + app.version : undefined}>tasktracker</span>
    {#if app.ready}
      <SegmentedControl options={views} value={app.view as View} onchange={setView} ariaLabel="View" />
    {/if}
  {/snippet}
  {#snippet right()}
    {#if app.ready}
      <!-- Off while a write is in flight, when the page would drop the
           click and the switch would show a state the rows do not. -->
      <Toggle checked={app.showAll} disabled={app.inFlight} label="Show archived" onchange={() => void app.toggleShowAll()} />
      <Button size="sm" onclick={() => app.newProject()}>New project</Button>
      <Button size="sm" onclick={() => app.newArea()}>New area</Button>
    {/if}
    <ThemeToggle size="sm" />
  {/snippet}
</TopBar>

<main class="main">
  {#if app.fatal}
    <div class="pad">
      <Notice tone="error" title="Could not reach the server" message={app.fatal} />
    </div>
  {:else if !app.ready}
    <div class="pad muted">Loading…</div>
  {:else}
    <div class="panes">
      <section class="list" aria-label="Rows">
        <RowList {app} />
      </section>
      <section class="detail" aria-label="Detail">
        <Detail {app} />
      </section>
    </div>
  {/if}
</main>

<StatusBar>
  {#snippet left()}
    {#if app.error}
      <span class="error">error: {app.error}</span>
    {:else}
      <span class="status">{app.status}</span>
    {/if}
  {/snippet}
  {#snippet right()}
    {#if app.ready}
      <span class="help" title={app.helpLine()}>{app.helpLine()}</span>
    {/if}
  {/snippet}
</StatusBar>

{#key app.modal}
  {#if app.modal?.kind === "area"}
    <AreaForm {app} modal={app.modal} />
  {:else if app.modal?.kind === "project"}
    <ProjectForm {app} modal={app.modal} />
  {:else if app.modal?.kind === "task"}
    <TaskForm {app} modal={app.modal} />
  {:else if app.modal?.kind === "subtask"}
    <SubtaskForm {app} modal={app.modal} />
  {:else if app.modal?.kind === "confirmDelete"}
    <Confirm question={app.deleteQuestion(app.modal.row)} onanswer={(yes) => void app.confirm(yes)} />
  {:else if app.modal?.kind === "confirmState"}
    <Confirm question={app.stateQuestion(app.modal.row)} onanswer={(yes) => void app.confirm(yes)} />
  {/if}
{/key}

<style>
  .brand {
    font-weight: var(--font-weight-semibold);
    margin-right: var(--space-3);
  }

  .main {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
  }

  .pad {
    padding: var(--space-4);
  }

  .muted {
    color: var(--text-muted);
  }

  .panes {
    flex: 1;
    min-height: 0;
    display: grid;
    grid-template-columns: minmax(0, 55fr) minmax(0, 45fr);
  }

  .list,
  .detail {
    min-height: 0;
    overflow: auto;
  }

  .detail {
    border-left: var(--border-width) solid var(--border-default);
    padding: var(--space-3) var(--space-4);
  }

  .status {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .error {
    color: var(--accent-red);
  }

  .help {
    color: var(--text-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  @media (max-width: 720px) {
    .panes {
      grid-template-columns: minmax(0, 1fr);
      grid-template-rows: minmax(0, 1fr) auto;
    }

    .detail {
      border-left: none;
      border-top: var(--border-width) solid var(--border-default);
      max-height: 40%;
    }
  }
</style>
