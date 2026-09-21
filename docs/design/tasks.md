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

- A task has a title, a status, an optional due date and an optional
  link to a GitHub issue, and nothing else for now.
- Statuses: `todo`, `doing`, `done`, with `dropped` for things decided
  against.
- No priority, estimate, notes or comments. Those are the issue-tracker
  parts left out until they are missed.

### Issue links

Some tasks are the local end of work tracked on GitHub. The task keeps a
link to that issue so the discussion is one step away, and tasktracker
does nothing more with it: no fetching, no syncing of state, no token.
The task's own status is still set by hand.

- One link per task, optional. A task that spans several issues can use
  subtasks, or the one issue that gathers them.
- Only GitHub issue and pull request URLs are accepted, so a typo is
  caught when the link is typed rather than when it fails to open. The
  shorthand `owner/repo#N` is accepted too.
- The link is stored as the full https URL whatever form was typed, so
  every stored link opens as it is. The shorthand becomes an issues URL,
  which GitHub redirects to the pull request when the number is one.
- Displays that have little room show `owner/repo#N`; the TUI makes it a
  terminal hyperlink to the URL.
- A copy does not take the original's link. The issue belongs to the
  original's piece of work, and the copy is new work.

### Copying

Some tasks look like ones already listed: the same checklist, another
date. A copy saves retyping them. The copy takes the original's title,
due date and project, any of which can be changed on the way, starts as
todo with no issue link, and gets the original's subtasks unticked. The
original is not touched. There is no link between the two afterwards; a
copy is just a new task that started filled in.

### Archiving

Finishing a task used to hide it. That lost the record of what had been
done, and a slip of the space bar made a task vanish. Now a done or
dropped task stays in the tree, greyed out, and leaves only when it is
archived on purpose.

- Archiving is a flag on the task, separate from its status. Only a
  finished task can be archived: an open one is still work to do, and
  hiding it would lose it.
- An archived task is out of the tree and the due list by default, and
  shown again with the same toggle that shows finished projects.
- Reopening an archived task, by marking it todo or doing, brings it back
  out of the archive. There is no such thing as an archived open task.
- Projects keep their own rule: a done or shelved project is hidden with
  everything in it, after a confirmation.

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
- The same pane arranged by deadline: every open task as one list,
  soonest due first, undated tasks last, subtasks still under their task
  and the project name after the title. The list is split under headings
  for how soon: overdue, the next 7 days (from today), the next 30,
  longer, and no deadline, each with a count and left out when empty. The
  headings fold like areas do, with the same keys, and the folds last as
  long as the others. Done and dropped tasks are not due any more and
  are not what this view is for, so they are left out of it; the
  by-project view keeps them, greyed, until archived. One key switches
  between the two views, so the tree answers "what is there to do on
  this" and the list answers "what is due next".
- Finished tasks stay in the tree, greyed, until archived. Archived tasks
  and finished projects (done or shelved) are hidden by default and shown
  with a toggle, so the tree stays short.

## Open questions

1. **Day to day.** What to see first when opening the app. The tree opens
   first and the by-deadline list is one key away, until using it says
   otherwise.
2. **Tasks without a project.** For now every task needs a project; a
   catch-all project is one way round it if that turns out to be annoying.
3. **What "done" means for a project.** Whether a project can be marked
   done while it still has open tasks is left open; nothing stops it.
