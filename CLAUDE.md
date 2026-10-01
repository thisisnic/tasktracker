# tasktracker

A terminal task tracker: areas hold projects, projects hold tasks, tasks
hold subtasks. One Go binary with a Cobra CLI and a Bubble Tea TUI over a
SQLite file. README.md is the user-facing reference; docs/design/tasks.md
holds the design decisions and open questions.

## Layout

- `cmd/tasktracker` — main, wires config, store and the CLI.
- `internal/task` — the SQLite store and the outline tree. All writes go
  through `Store` methods; `migrate()` only adds columns.
- `internal/cli` — Cobra commands, every one with `--json`.
- `internal/tui` — Bubble Tea v2 model, huh forms, row list, cursor rules.
- `internal/goallink` — read-only reader of a goaltracker database.
- `internal/backup`, `internal/update`, `internal/version`,
  `internal/config` — shared plumbing, kept in step with goaltracker.

## Workflow

```bash
make vet && make test   # before every commit
make build              # ./tasktracker, gitignored
```

Every commit is reviewed by roborev from a post-commit hook. The loop is:

1. Run the suite. Commit or amend **only after a green run**. Chain it so
   red tests cannot be committed:
   `go test ./... >/dev/null 2>&1 && git commit --amend --no-edit`
2. `roborev wait <sha>` then `roborev show <sha>`.
3. Fix each finding, amend, and repeat until "No issues found".

The hook only fires for a plain commit. An amended or rebased commit
gets no review on its own, so queue one: `roborev review <sha>`. With
several unpushed commits, put a fix in the commit it belongs to with
`git commit --fixup=<sha>` and
`GIT_SEQUENCE_EDITOR=true git rebase -i --autosquash <sha>^`, then
review every rewritten sha. The TUI suite takes about 40 s.

Docs move with the code in the same commit: the README key list, feature
notes and commands table, the root command's help text in
`internal/cli/root.go`, and docs/design/tasks.md. Design notes are
written as decisions and reasons, not as quoted conversation. Example
names are neutral ("home", "garden", "maintenance"); nothing from anyone's
workplace goes into docs, help text, comments or fixtures.

Releases are cut by tagging `vX.Y.Z` and pushing the tag. A behaviour
change bumps the minor version.

## What the owner wants

- A new attribute on a task, project or area is a field in that thing's
  existing form, edited like the others. Not a key of its own, not an
  editor of its own, nothing on the help line.
- Build what was asked for. Do not copy a goaltracker TUI feature
  across because it is there; only the shared plumbing packages are
  kept in step. Do not add ways out to other programs, such as an
  `$EDITOR` hook.

## Smoke testing

Only ever on a scratch database:

```bash
TASKTRACKER_DB=/tmp/scratch/smoke.db XDG_CONFIG_HOME=/tmp/scratch/cfg \
  GOALTRACKER_DB=/tmp/scratch/absent.db ./tasktracker
```

Never point the binary at real data while testing.

## What review keeps finding

These are the themes roborev raises most often. Check for them before
committing rather than after.

- **Every state change says where the cursor lands**, in both the by
  project and by deadline views, including rows that fold away or
  disappear (headings, finished tasks, deleted rows, folded buckets). Use
  the same rule everywhere: `selectNear`, the nearest container, the
  soonest due task. Test the landing, with fixtures where tree order and
  due order disagree.
- **Status line, help line and hints match the current view.** A hint
  written for by project is often wrong by deadline (what `f` toggles,
  whether a finished task stays listed, where `z` can archive it). Check
  every message that names a key against what that key does in each view.
- **Put conditions in the SQL, not around it.** The CLI and TUI can be
  open at once, so check-then-write in the store is a race. Archiving
  checks status inside the UPDATE; copying reads the original inside the
  transaction and copies subtasks with INSERT..SELECT.
- **No dead or untested paths.** Unused edit fields, unused parameters,
  dead assignments and unreachable fallbacks get flagged. Remove them or
  test them.
- **One traversal helper, not copies.** Walk the tree with `eachTask`,
  look things up with `findTask`, order by due with `sooner`. If a new
  loop looks like one of these, use the helper.
- **Comments state the real reason.** When behaviour changes, the comment
  that explained the old behaviour must change with it.
- **Tests.** A fresh struct per `json.Unmarshal` (omitempty keeps old
  values). A test's comment must match what it asserts. Mutually
  exclusive flags are declared with `MarkFlagsMutuallyExclusive` and
  the rejection is tested. A check that something did *not* happen must
  also prove the path was reached: assert the exact status it ends on,
  not a prefix, and the exact cursor target, not just its kind.
- **Messages that accumulate.** Anything appended to the status line by
  a timer or a repeat must be added once, and must not wipe the last
  action's message or error. Timers clear only their own errors.
- **Migrations only add columns.** `migrate()` never rewrites data; a
  new column gets a default that leaves existing rows meaning what they
  meant before.
