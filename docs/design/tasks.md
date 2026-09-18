# Tasks

Design notes for tasktracker. Written from the owner's description, in their
terms. Ideas that came from the builder rather than the owner are marked as
suggestions. Anything not settled is listed under Open questions rather than
decided.

## Context

goaltracker holds goals for the year, quarter and month. tasktracker is the
day-to-day work: "we model it a bit like jiras, ya know? but not exactly."
It is a separate binary with its own database, built the same way.

## Hierarchy

Three levels of work: **project → task → subtask**, with **areas** above
projects for grouping.

- A project holds tasks. A task holds subtasks.
- Every task belongs to a project, and every subtask to a task. (Whether a
  task can exist with no project was not answered; see Open questions.)
- A project can sit in an area, and areas nest. See Areas.

## Areas

Added on 2026-09-17, once real work was going in: "I have arrow work, some
of it is STF work and some is general maintenance. I'd want to have STF
stuff with 2 sub-levels, and then within those there would be projects."

- "The things above projects are more like tags but i still want some
  hierarchy." So an area is just a name and, optionally, the area it sits
  in. No state, no due date, no description.
- An area holds sub-areas and projects, mixed. A project is in at most one
  area; a project in none sits at the top level, which is where every
  project made before areas existed stays.
- Areas roll up their open task count, and nothing else. They cannot be
  marked done or shelved; a finished branch is one whose projects are all
  finished.
- Deleting an area is like removing a tag: what was in it moves up a
  level. Nothing in it is deleted.
- Goal links stay on projects for now (builder's suggestion, to be
  revisited if STF-style work wants a goal at the area level).

### Collapsing

"I don't always need to view the full hierarchy btw as it'll truncate
stuff on the screen, but i do wanna be able to place things so I can choose
to view by an area at diff level if I want."

The first answer was zooming the tree to one area. That was replaced, at
the owner's request, with folding: the left or right arrow on a project
hides its tasks, and on an area hides everything in it ("make areas fold
too"), leaving the row with a `▸` and its open count. The same key shows
them again ("can we have the same key to expand/close", "why not just the
arrows"). On a task or subtask it folds the project the row is in. The
whole tree
stays in view, so nothing is out of sight; only the detail is.

## Projects

- A project has a name and a description.
- "Projects do have end states." Suggested states: `active`, `done` and
  `shelved`. The owner will rename these if they do not fit.
- A project can link to goals in goaltracker: "ooh, a project can link to a
  goal from the goal tracker", and "a project could serve several goals". So
  the link is zero or more goals per project.

## Tasks

- A task has a title, a status and an optional due date: "just status and
  due date for now."
- Suggested statuses: `todo`, `doing`, `done`, with `dropped` for things
  decided against. The owner's response: "cool, i'll update if i don't like."
- Nothing else on a task for now: no priority, estimate, notes or comments.
  Those are the Jira parts left out until they are missed.

## Subtasks

- "A subtask is maybe a checklist, sure." A subtask is a title and a tick.
  It has no status or due date of its own.
- Ticking every subtask does **not** finish the task: "don't autoclose." The
  owner marks the task done themselves.

## Linking to goals

Loose coupling, agreed with the owner: a project stores the goaltracker goal
ids it serves. When a project is shown, tasktracker reads goaltracker's
database read-only to put the goal's statement next to the id. If that
database cannot be read, only the ids are shown. Nothing in goaltracker
changes.

The owner asked whether goaltracker could be told never to renumber goals.
It never renumbers an existing goal. The one gap is that SQLite may reuse the
id of the most recently deleted goal for the next new one. Making the goals
table `AUTOINCREMENT` closes that gap; it is a one-line change in goaltracker,
left as a follow-up there.

## Showing tasks

The owner prefers the TUI to the CLI, so the TUI is the main way in. The CLI
exists for scripts and coding agents, with `--json`.

Suggested layout, to be adjusted once real tasks are in it:

- A tree pane: areas, then projects as headers, their tasks indented under
  them, and subtasks under tasks with a tick box. A detail pane for the
  selected item.
- A due view that lists open tasks with a due date, soonest first, overdue
  ones marked. This is the builder's reading of "maybe what's due etc".
- Finished things (done or dropped tasks, done or shelved projects) are
  hidden by default and shown with a toggle, so the tree stays short.

## Open questions

1. **Day to day.** What the owner wants to see first when opening the app
   was "maybe what's due etc, idk exactly". The tree opens first and the due
   view is one key away, until using it says otherwise.
2. **Tasks without a project.** Not answered. For now every task needs a
   project; a catch-all project is one way round it if that turns out to be
   annoying.
3. **What "done" means for a project.** Whether a project can be marked done
   while it still has open tasks is left to the owner; nothing stops it.
