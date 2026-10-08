<script lang="ts">
  import { onMount } from "svelte";
  import { Button, DetailDrawer, Notice, SegmentedControl, StatusBar, ThemeToggle, Toggle, TopBar } from "@kenn-io/kit-ui";
  import { AppState } from "./lib/app.svelte";
  import type { View } from "./lib/rows";
  import RowList from "./components/RowList.svelte";
  import Detail from "./components/Detail.svelte";
  import AreaForm from "./components/AreaForm.svelte";
  import ProjectForm from "./components/ProjectForm.svelte";
  import TaskForm from "./components/TaskForm.svelte";
  import SubtaskForm from "./components/SubtaskForm.svelte";
  import Confirm from "./components/Confirm.svelte";

  // The build this page's code is from, as the server named it in
  // index.html; empty under the dev server, which serves the file as
  // written, and then the server's word is taken.
  const page = document.querySelector('meta[name="page"]')?.getAttribute("content") ?? "";
  const app = new AppState(undefined, undefined, undefined, page);

  onMount(() => {
    void app.start();
  });

  // The drawer's header names the kind of row; the row's own name is
  // the heading inside.
  const drawerTitle = $derived.by(() => {
    const r = app.selected();
    if (!r) return "";
    return r.kind === "heading" ? "Due" : r.kind[0]!.toUpperCase() + r.kind.slice(1);
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
        void app.advance();
        break;
      case "Enter":
        app.toggleDrawer();
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
      <Button size="sm" disabled={app.inFlight} onclick={() => void app.archiveFinished()}>Archive finished</Button>
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
    <RowList {app} onrow={() => app.open()} />
  {/if}
</main>

<!-- The selected row's detail, opened with enter and closed with
     Escape, the overlay or its button. It is put away while a form or
     question is open, so Escape closes that and not it. -->
{#if app.drawer && app.selected() && !app.modal}
  <DetailDrawer title={drawerTitle} onclose={() => app.closeDrawer()} width="min(480px, 100vw)">
    <Detail {app} />
  </DetailDrawer>
{/if}

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
    overflow: auto;
  }

  .pad {
    padding: var(--space-4);
  }

  .muted {
    color: var(--text-muted);
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

</style>
