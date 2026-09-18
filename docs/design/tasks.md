# Tasks

Design notes for tasktracker: what the pieces are, why they are shaped the
way they are, and what is still open. Anything not settled is listed under
Open questions rather than decided.

## Context

goaltracker holds goals for the year, quarter and month. tasktracker is the
day-to-day work: loosely modelled on an issue tracker, without most of its
parts. It is a separate binary with its own database, built the same way.

## Hierarchy

Three levels of work: **project → task → subtask**, with **areas** above
projects for grouping.

- A project holds tasks. A task holds subtasks.
- Every task belongs to a project, and every subtask to a task. (Whether a
  task can exist with no project is not settled; see Open questions.)
- A project can sit in an area, and areas nest. See Areas.

## Areas

Once there are enough projects, they want grouping: one line of work with
a couple of sub-levels, and projects inside those. The grouping is closer
to tags than to a strict hierarchy, but the hierarchy still matters for
placing things.

- An area is just a name and, optionally, the area it sits in. No state,
  no due date, no description.
- An area holds sub-areas and projects, mixed. A project is in at most one
  area; a project in none sits at the top level, which is where every
  project made before areas existed stays.
- Areas roll up their open task count, and nothing else. They cannot be
  marked done or shelved; a finished branch is one whose projects are all
  finished.
- Deleting an area is like removing a tag: what was in it moves up a
  level. Nothing in it is deleted.
- Goal links stay on projects for now. To be revisited if a whole area
  turns out to serve one goal.

### Folding

The full hierarchy is more than fits on a screen, so the tree needs a way
to show less of it without losing where things are.

The first answer was zooming the tree to one area. That was replaced with
folding: the left or right arrow on a project hides its tasks, and on an
area hides everything in it, leaving the row with a `▸` and its open
count. The same key shows them again. On a task or subtask it folds the
project the row is in. The whole tree stays in view, so nothing is out of
sight; only the detail is. Folds last for the session, and saving
something inside a fold unfolds the way to it.

## Projects

- A project has a name and a description.
- Projects have end states: `active`, `done` and `shelved`. The names can
  change if they do not fit.
- A project can link to goals in goaltracker, and can serve several, so
  the link is zero or more goals per project.

## Tasks

- A task has a title, a status and an optional due date, and nothing else
  for now.
- Statuses: `todo`, `doing`, `done`, with `dropped` for things decided
  against.
- No priority, estimate, notes or comments. Those are the issue-tracker
  parts left out until they are missed.

## Subtasks

- A subtask is a checklist item: a title and a tick. It has no status or
  due date of its own.
- Ticking every subtask does **not** finish the task. The task is marked
  done by hand.

## Linking to goals

Loose coupling: a project stores the goaltracker goal ids it serves. When
a project is shown, tasktracker reads goaltracker's database read-only to
put the goal's statement next to the id. If that database cannot be read,
only the ids are shown. Nothing in goaltracker changes.

goaltracker never renumbers an existing goal. The one gap is that SQLite
may reuse the id of the most recently deleted goal for the next new one.
Making the goals table `AUTOINCREMENT` closes that gap; it is a one-line
change in goaltracker, left as a follow-up there.

## Showing tasks

The TUI is the main way in. The CLI exists for scripts and coding agents,
with `--json`.

Layout, to be adjusted as it gets used:

- A tree pane: areas, then projects as headers, their tasks indented under
  them, and subtasks under tasks with a tick box. A detail pane for the
  selected item.
- A due view that lists open tasks with a due date, soonest first, overdue
  ones marked.
- Finished things (done or dropped tasks, done or shelved projects) are
  hidden by default and shown with a toggle, so the tree stays short.

## Open questions

1. **Day to day.** What to see first when opening the app. The tree opens
   first and the due view is one key away, until using it says otherwise.
2. **Tasks without a project.** For now every task needs a project; a
   catch-all project is one way round it if that turns out to be annoying.
3. **What "done" means for a project.** Whether a project can be marked
   done while it still has open tasks is left open; nothing stops it.
