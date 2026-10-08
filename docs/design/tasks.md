# Tasks

Design notes for tasktracker: what the pieces are, why they are shaped the
way they are, and what is still open. Anything not settled is listed under
Open questions rather than decided.

## Context

tasktracker is the day-to-day work: loosely modelled on an issue tracker,
without most of its parts. One binary with its own database, built the
same way as its siblings.

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

### Folding

The full hierarchy is more than fits on a screen, so the tree needs a way
to show less of it without losing where things are.

The first answer was zooming the tree to one area. That was replaced with
folding: the left or right arrow on a project hides its tasks, and on an
area hides everything in it, leaving the row with a `▸` and its open
count. The same key shows them again. On a task or subtask it folds the
project the row is in. The whole tree stays in view, so nothing is out of
sight; only the detail is. Saving something inside a fold unfolds the
way to it.

Folds are kept between sessions. They are the owner's arrangement of the
tree, and opening the app to find it all unfolded again made the folds
not worth making. They live in a small file beside the database, one
fold per line, rather than in the database itself: they are state of
this screen on this machine, not data, so they stay out of the backup
and out of the CLI's view, and a fold does not count as a change for
the backup on quit. A file beside the database, like SQLite's own -wal
and -shm files, means a scratch database has its own folds. The file is
rewritten on every fold and unfold, so a session that ends without a
clean quit still keeps them. A fold on something since deleted is
dropped the next time the tree is loaded, as it was within a session.
Between sessions an id can come back: SQLite gives a deleted row's id to
the next row made, so a kept fold also carries the stamp of the row's
creation, and is dropped when the row with that id was made at another
time. A row deleted and replaced within the same second keeps the old
one's fold, which one key undoes. A line in the file that is not a fold
is skipped and reported; the rest are still used. Two UIs open at once
on the same database each write the file whole from their own folds,
read once at the start, so the last UI to change a fold writes its set
and the other UI's folds from that session are lost.

## Projects

- A project has a name and a description.
- Projects have end states: `active`, `done` and `shelved`. The names can
  change if they do not fit.

## Tasks

- A task has a title, a status, an optional due date, an optional link
  to a GitHub issue and a block of notes, and nothing else for now.
- Statuses: `todo` and `done`, with `dropped` for things decided
  against. There was a `doing` between them at first, and it was never
  set on purpose: the list is of things to do, and the moment of
  starting one is not worth a keypress. A task is done or it is not,
  which is what a checkbox says, and the page's rows are checkboxes.
  Retiring it is the one migration that rewrites rows, since no added
  column could leave a doing task meaning what it meant: a doing task
  was an open one, so it becomes `todo`.
- No priority, estimate or comments. Those are the issue-tracker parts
  left out until they are missed. Notes were the first to be missed.

### Notes

A task often has something worth keeping next to it: where a thing is,
what was agreed, what to try next. The notes are one block of free text
on the task, not dated entries; a running log is what comments would be,
and those are still left out.

- Notes are a field in the task form like the title and the due date,
  rather than an editor of their own, so there is one way to change a
  task. The field is a few lines tall and scrolls; a new line is
  `ctrl+j`, since enter moves to the next field as it does everywhere in
  the form.
- Blank lines at either end are dropped on save, so an accidental enter
  does not leave a gap. A leading indent is kept.
- The detail pane shows the notes after the subtasks, which are shorter
  and the thing to tick off; the row carries a `≡` so a task with notes
  can be told from one without before it is selected.
- A copy takes the original's notes along with its title and subtasks,
  since they describe the work; the copy form shows them to be changed.

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
due date, notes and project, any of which can be changed on the way,
starts as todo with no issue link, and gets the original's subtasks
unticked. The
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
- Reopening an archived task, by marking it todo, brings it back out of
  the archive. There is no such thing as an archived open task.
- Projects keep their own rule: a done or shelved project is hidden with
  everything in it, after a confirmation.

## Subtasks

- A subtask is a checklist item: a title and a tick. It has no status or
  due date of its own.
- Ticking every subtask does **not** finish the task. The task is marked
  done by hand.

## Goal links, removed

Projects used to carry goaltracker goal ids and read goaltracker's
database for their statements. The link was dropped: tasktracker stands
alone. An old database keeps its `project_goals` table, unread.

## Showing tasks

The TUI and the browser page (see Browser UI) are the ways in. The CLI
exists for scripts and coding agents, with `--json`.

Layout, to be adjusted as it gets used:

- A tree pane: areas, then projects as headers, their tasks indented under
  them, and subtasks under tasks with a tick box. A detail pane for the
  selected item. The page has the same tree, with the detail in a
  drawer instead of a pane.
- The same pane arranged by deadline: every open task as one list,
  soonest due first, undated tasks last, subtasks still under their task
  and the project name after the title. The list is split under headings
  for how soon: overdue, the next 7 days (from today), the next 30,
  longer, and no deadline, each with a count and left out when empty. The
  headings fold like areas do, with the same keys, and the folds are
  kept with the others. Done and dropped tasks are not due any more and
  are not what this view is for, so they are left out of it; the
  by-project view keeps them, greyed, until archived. One key switches
  between the two views, so the tree answers "what is there to do on
  this" and the list answers "what is due next".
- Finished tasks stay in the tree, greyed, until archived. Archived tasks
  and finished projects (done or shelved) are hidden by default and shown
  with a toggle, so the tree stays short.

## Changes from elsewhere

The CLI and the UI can be open on the same database at once, and a
coding agent on the CLI is the usual case. The UI used to show what it
loaded until a key reloaded it, so a task added by the agent was invisible
until the owner pressed something that happened to reload.

- The UI polls: every couple of seconds it reads SQLite's data version,
  which moves when another connection commits and not when its own does,
  and reloads the rows when it has moved. The check is one pragma, so it
  is cheap enough to do often; the reload only happens on a change.
- The cursor follows the same rule as after any other change: it stays
  on the same row where that row still exists, and otherwise keeps its
  place. The status line says the database was changed elsewhere, so a
  row that moved or vanished is explained; it says so after whatever
  reload took the change in, a poll or a key such as `r` or a save, and
  after that reload's own message.
- While a form or a confirmation is open the rows are not reloaded. The
  form holds what was typed, and a confirmation names the row it will act
  on, so neither should have the rows move under it. The next poll after
  it closes picks the change up; a save reloads anyway.
- A change from elsewhere counts as a change for the backup on quit, so a
  session that watched an agent work still backs up as it closes. It is
  counted by the reload itself, whatever prompted it, so a change taken
  in by `r`, `f` or `v` before the next poll is not missed; and the UI
  takes one last look at the version as it quits, for a change that
  landed after the final poll or while a form was open.

## Backups on quit

The UI can back up as it closes. It used to do that every time, and the
backup package would then find the database unchanged and write nothing;
but the snapshot was still taken, and with git on the push was still
tried. Most sessions only look. So the UI keeps track of whether the
database changed while it was open, by its own writes or by another
process it saw, and on quit skips the whole step when it did not, saying
so in one line.

- The rule is "did the database change while the UI was open", by the UI
  or by another process it saw, not "is the backup up to date". A change
  made by the CLI while no UI was open is kept by the next session that
  changes something, by the server's next scheduled run, or by running
  the backup command; the last two check the database itself. That
  trade is taken knowingly: the on-quit backup is a convenience for the
  common case, and the server and the command are there for the rest.
- A write that fails, a cancelled form and a declined confirmation do
  not count; a save that changed nothing does, since the store was
  written to.
- With git on, a push that failed is retried on every later run, and a
  quit that changed nothing is one of them: the push is skipped when
  there is nothing to push, so it costs no more than a few local git
  commands. Before the first backup there is no repo to push from, so
  nothing is tried.

## Update notices

`tasktracker update` installs the latest release, but nobody runs it
without a reason, so releases sat uninstalled. Every run now checks, in
both the CLI and the UI, and says when a newer release is out.

- One answer a day. The check asks GitHub for the latest release at most
  once in twenty-four hours and keeps the answer in the user's cache
  directory, so every run can say whether a newer release is out without
  a network call. The notice is therefore shown on every run until the
  update is installed, which is the point: a notice that comes once is
  missed.
- The check starts once the command to run is known and runs in the
  background, so the command's own work overlaps it. A command waits for
  the answer only at its end, and only on the day's one run that goes to
  the network; the network call has a five second bound, so that is the
  most a command is ever delayed. Shell completion is read by the shell,
  not a person, and must never wait on the network, and the update
  command asks GitHub itself, so neither starts a check.
- The CLI says it on stderr, after the command's output, so `--json` on
  stdout stays clean and the notice is the last thing on the screen. A
  command that failed says nothing: its error is what wants reading, and
  the next command that works carries the notice.
- The UI puts the notice in the title line, not the status line. The
  status line is for the last action and is overwritten by the next; the
  title line stays, and is the one place the UI can say something for a
  whole session without getting in the way. On a narrow terminal the
  title is cut from the right, so the notice goes last and the view's
  name is kept.
- A build that is not a release has nothing to compare with, so it is
  not checked and nothing is fetched. A pseudo-version after a tag, as
  `go install @main` gives, is a build from main, and is compared like
  any other.
- Trouble is not reported. A check that cannot reach GitHub keeps the
  last answer and tries again an hour later rather than in a day, so a
  release is not missed over a moment offline and an afternoon offline
  does not cost every command a five second wait. `tasktracker update
  --check` is there for anyone who wants to know what went wrong.

## Browser UI

The terminal UI was the main way in. A browser page is the other: it can
be a browser's home page, so the day's tasks are the first thing seen,
and a mouse can do what the keys do. The decisions follow ghrepotracker,
which went this way first, so the two feel the same.

- The bare command serves. `tasktracker` binds a loopback port, serves
  the page and its API, and opens a browser tab; `tasktracker tui` opens
  the terminal UI. The page is the way in from now on, and a home page
  that needs a flag to appear is not one. The port is in the config,
  `port`, and `--port` overrides it for one run; a fixed port keeps the
  bookmark working. A taken port is an error rather than a random one,
  for the same reason.
- One API, the store's own shapes. Every read is the object the CLI's
  `--json` prints, and the outline is `task list --json` exactly, so a
  script can learn one vocabulary. Every write is one `Store` method,
  so the CLI, the terminal UI and the browser cannot disagree about what
  a change means. An edit sends only the fields it changes; a status
  change on its own goes through the one-UPDATE `MarkTask`, and a
  project's state change through `MarkProject`, as the terminal UI's
  space and x keys do, so neither can write back a title or a name read
  a moment before another process changed it.
- Errors are told apart. The store's input errors match `ErrInvalid`
  and answer 400; `ErrNotFound` answers 404; anything else is 500. The
  messages are the CLI's, unchanged: `invalidError` reads as its
  message and only matches the sentinel. A project or area the body
  names that does not exist matches both and answers 400, so that 404
  always means the thing at the address.
- No release notice on the page. The terminal UI shows one in its
  title line from the check the command starts; the server starts the
  same check and says its answer on stderr when it stops, which for a
  server left running is rarely. Showing it on the page would need the
  server to check again every day, which it does not do yet; whether
  it should is an open question.
- Only this machine, only this page. The listener is 127.0.0.1; a Host
  header that is not loopback is refused, so a name rebound to loopback
  cannot read the tasks; every write must declare a JSON body, which a
  form cannot, and an Origin from elsewhere is refused.
- Changes from elsewhere reach the page the way they reach the terminal
  UI: the page polls a version and reloads when it has moved. The
  version is SQLite's data version, which moves when another process
  commits, joined with the server's own count of writes, since the
  store is one connection and SQLite reports other connections' commits
  only: without the count, a second tab would never see what the first
  tab changed. The server's start time goes in front of both, since
  both start again when it is restarted, and a page left open across
  the restart would else miss what was written while it was down. A
  page still reloads after its own writes without waiting for the poll.
- The page shares the terminal UI's behaviour, not its looks. The rows
  come in the same order, the keys do the same things, the status line
  says the same words, and the rules for where the selection lands
  after a change are ported line for line, so the two UIs agree and
  there is one set of rules to reason about. How it looks follows web
  practice instead: one list in the page's own typeface, a checkbox on
  every task and subtask, a chevron to fold, a chip for a due date, a
  form that opens on a click, and a drawer that opens on enter with the
  detail and the actions as buttons. A click edits, as on any page; a
  heading has no form, so its click opens the drawer. A task's box is
  ticked when done and clear otherwise, so a click on a dropped task's
  box ticks it done, where space would reopen it. A first version
  copied the terminal UI's two panes, monospace and glyphs, and read as
  a terminal in a browser, which was not the point of having a page. A
  form sends only the fields that changed, so a stored issue the server
  would refuse does not block an edit, as the terminal UI's form keeps
  it.
- Archive finished, in the top bar, archives every finished task in an
  active project in one UPDATE; a finished project is out of sight with
  everything in it already. It is the page's alone until a scope is
  missed.
- The page's folds live in the browser's storage, not the terminal
  UI's file. Folds are the state of a screen, and the page's screen is
  the browser; the two UIs are different screens and keep their own
  arrangement. They are matched by the row's creation stamp as the file's
  are, so a deleted row's fold cannot land on whatever next reuses its
  id. A fold whose row is not listed is kept rather than pruned, since
  a finished project hidden until `f` still exists; checking every
  project would be another request per reload for nothing.
- The server backs up on a schedule, not when it stops. The terminal UI
  backs up on quit because quitting ends a session of changes; the
  server is left running for days as a home page, and stopping it is
  not the end of anything, so a snapshot then would be rare and badly
  timed. Instead, with `[backup]` configured, the serving process backs
  up once as it starts and then every 12 hours for as long as it runs.
  The start-up run catches changes made from the CLI while nothing was
  serving; the marker makes a quiet run free. The 12 hours are
  wall-clock time, checked hourly: Go's timers stop with the machine, so
  a plain 12-hour ticker on a laptop would count only hours awake. A
  failed run is reported on stderr and retried when the interval next
  passes rather than stopping the server; a run cut short by shutdown is
  not reported as a failure, though a commit that landed without its
  push still says so, since the next run pushes it. The interval is
  fixed: there is nothing to tune until use shows otherwise. A broken
  config starts no loop, since it names no backup folder; the note that
  the config is being ignored covers it. `tasktracker backup` is still
  there for a snapshot right now, and shares the marker. The two can
  overlap: both would write the same snapshot, and with `git = true` one
  may lose to the other's index lock. The automatic run simply retries
  later; a manual run that fails that way is rerun by hand.
- The page is built into the binary. Releases build it first, so the
  downloaded binary serves it; a plain `go install` cannot, and serves a
  notice saying so in its place. Needing bun to build from source is the
  price of one binary with no files beside it.

## Open questions

1. **Day to day.** What to see first when opening the app. The tree opens
   first and the by-deadline list is one key away, until using it says
   otherwise. The page opens the same way; as a home page it may want
   to remember the last view.
2. **Tasks without a project.** For now every task needs a project; a
   catch-all project is one way round it if that turns out to be annoying.
3. **What "done" means for a project.** Whether a project can be marked
   done while it still has open tasks is left open; nothing stops it.
4. **Release notices while serving.** The page shows none. A server
   that checks daily and a line in the page's top bar would match what
   the terminal UI does.
