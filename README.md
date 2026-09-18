# tasktracker

[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/thisisnic/tasktracker)](https://github.com/thisisnic/tasktracker/releases)
[![ci](https://github.com/thisisnic/tasktracker/actions/workflows/ci.yml/badge.svg)](https://github.com/thisisnic/tasktracker/actions/workflows/ci.yml)

**[Releases](https://github.com/thisisnic/tasktracker/releases)** | **[Quick Start](#quick-start)** | **[Backups](#backups)**

A personal task tracker that lives in your terminal and keeps your data on
your machine. Projects hold tasks, tasks hold checklists, and a project can
point at the goals it serves in [goaltracker](https://github.com/thisisnic/goaltracker).
One binary, one SQLite file, no account.

```text
tasktracker · tree
╭─────────────────────────────────────────────────────╮╭───────────────────────────────────────────╮
│ house                                        2 open ││ paint the hall                            │
│   ○ paint the hall                  1/2  2026-09-10 ││ project house                             │
│     [x] buy paint                                   ││ status  todo   id #1                      │
│     [ ] move furniture                              ││ due     2026-09-10  7 days overdue        │
│   ○ fix the gate                         2026-09-20 ││                                           │
│ work                                         1 open ││ subtasks 1/2                              │
│   ○ email accountant                                ││   [x] buy paint                           │
│                                                     ││   [ ] move furniture                      │
╰─────────────────────────────────────────────────────╯╰───────────────────────────────────────────╯
A project · a task · s subtask · e edit · space next status/tick · x drop · d delete · f finished · v due · j/k move · q quit
```

## How It Works

1. Make a project. Give it a description and, if you like, the ids of the
   goaltracker goals it serves.
2. Add tasks to it. A task has a title, a status (`todo`, `doing`, `done`
   or `dropped`) and an optional due date.
3. Break a task into subtasks when it helps: a checklist of titles with a
   tick each. Ticking every item does not finish the task; you mark that
   yourself.
4. Mark a project `done` or `shelved` when it is over. Finished projects
   and tasks are hidden from the tree until you ask for them.
5. Once there are enough projects, group them into areas. An area is a
   name that holds projects and other areas, so "home" can hold "garden" and
   "maintenance" with projects inside each. Fold an area or a project
   when what is in it is in the way.

The design notes behind this are in
[docs/design/tasks.md](docs/design/tasks.md).

## Quick Start

```bash
tasktracker                      # open the terminal UI
```

In the UI: `n` add an area, `A` add a project, `a` add a task, `s` add a
subtask, `e` edit, `space` step a task's status or tick a subtask, `x` drop
a task or shelve a project, `d` delete, `f` show finished, `v` switch to the
due list, `←`/`→` fold or unfold an area or project, `j`/`k` move, `q`
quit.
On a project, `space` steps active → done → shelved, but asks `y/N` before
leaving active, since a done or shelved project is hidden with every task
in it.

The same data is there on the command line, with `--json` for scripts and
coding agents:

```bash
tasktracker area add "home"
tasktracker area add "garden" --in 1
tasktracker project add "house" --description "fix it up" --goal 3
tasktracker project add "grant report" --in 2
tasktracker task add "paint the hall" --project 1 --due 2026-10-01
tasktracker subtask add 1 "buy paint"
tasktracker subtask tick 1
tasktracker task mark 1 doing
tasktracker task list --due --json
```

## Features

- **Three levels, one tree** - Projects, tasks and subtasks, shown as a
  tree with open counts and due dates at a glance.
- **Areas** - Group projects under names that nest as deep as you like.
  An area is a label with a place in the tree, nothing more: no state, no
  dates. Deleting one moves what was in it up a level.
- **Folding** - `←` or `→` on a project hides its tasks, and on an area
  hides everything in it, behind a `▸` that keeps the open count; press it
  again to show them. Folds last until you quit.
- **Due list** - Press `v` for every open task with a due date, soonest
  first, overdue ones in red.
- **Checklists** - Subtasks are a title and a tick. The task's row shows
  how many are done.
- **Goal links** - A project stores the ids of the goaltracker goals it
  serves. tasktracker reads goaltracker's database read-only to show their
  statements, and offers them as a pick list in the project form. Without
  that database the ids are shown bare and nothing else changes.
- **Encrypted backups** - One age-encrypted file, written to a folder you
  choose, and optionally committed and pushed to your own private git repo
  every time the UI exits.
- **Agent friendly** - A plain CLI with JSON output, so a coding agent can
  read and update your tasks without touching the UI.
- **Local and portable** - A single static binary and a single SQLite file
  at `~/.local/share/tasktracker/tasktracker.db` (or under `$XDG_DATA_HOME`).
  Point elsewhere with `--db` or `TASKTRACKER_DB`.
- **Self-updating** - `tasktracker update` fetches the latest release,
  verifies it against the published checksums, and swaps the binary in place.

## Goal Links

goaltracker's goals have numeric ids. Give a project those ids with
`--goal` (repeatable) or pick them in the project form, and tasktracker
looks the statements up in goaltracker's database, which it never writes
to. It looks in `$GOALTRACKER_DB`, then goaltracker's default location. If
yours is somewhere else, say so in the config:

```toml
[goaltracker]
db = "~/somewhere/goaltracker.db"
```

## Backups

A backup is an encrypted copy of the database written as one file,
`tasktracker.db.age`, into a folder you choose. Make that folder a private
git repo and the repo's history is the backup history. It can be the same
folder goaltracker backs up into; the files have different names. Set up
once:

```bash
tasktracker key new
```

That writes an age private key to `~/.config/tasktracker/key.txt` and prints
your public key together with a config to copy into
`~/.config/tasktracker/config.toml`:

```toml
[backup]
dir = "~/tasktracker-data"                      # your private data repo
recipient = "age1..."                           # public key, encrypts
identity_file = "~/.config/tasktracker/key.txt" # private key, decrypts
on_quit = true                                  # back up every time the UI exits
git = false                                     # true: git commit and push from dir too
```

Keep a copy of the private key in a password manager. Without it the backups
cannot be opened. Then:

```bash
tasktracker backup            # back up now (skipped if unchanged)
tasktracker restore           # put the backup in place of the database
```

With `git = true` tasktracker commits and pushes the file after each backup.
The data repo needs a remote and credentials that work without a prompt, such
as an SSH key in an agent. A failed push is reported and retried next time.

## Installation

**Binary (macOS / Linux / Windows):** download the archive for your platform
from [GitHub Releases](https://github.com/thisisnic/tasktracker/releases),
check it against `checksums.txt`, and put `tasktracker` on your `PATH`.

**With Go:**

```bash
go install github.com/thisisnic/tasktracker/cmd/tasktracker@latest
```

**Updating:**

```bash
tasktracker version           # what you have
tasktracker update --check    # is there a newer release?
tasktracker update            # install it
```

## Commands

| Command | What it does |
| --- | --- |
| `tasktracker` | Open the terminal UI |
| `tasktracker area add NAME` | Add an area; `--in` puts it inside another, `--json` |
| `tasktracker area list` | Areas as a tree with project counts; `--json` |
| `tasktracker area edit ID` | Rename or move an area; `--name`, `--in`, `--top` |
| `tasktracker area delete ID` | Delete an area; what is in it moves up a level; `--yes` |
| `tasktracker project add NAME` | Add a project; `--description`, `--in` (area), `--goal` (repeatable), `--json` |
| `tasktracker project list` | List active projects; `--all`, `--state`, `--json` |
| `tasktracker project show ID` | One project with its goals and tasks; `--json` |
| `tasktracker project edit ID` | Change name, description, area or goals; `--in` / `--top` move it, `--goal` replaces the set, `--no-goals` clears it |
| `tasktracker project mark ID active\|done\|shelved` | Set a project's state |
| `tasktracker project delete ID` | Delete a project with all its tasks; `--yes` |
| `tasktracker task add TITLE --project ID` | Add a task; `--due`, `--json` |
| `tasktracker task list` | Show the tree of areas, projects and open tasks; `--all`, `--project`, `--status`, `--due`, `--json` |
| `tasktracker task show ID` | One task with its subtasks; `--json` |
| `tasktracker task edit ID` | Change title, due date or project; `--no-due` |
| `tasktracker task mark ID todo\|doing\|done\|dropped` | Set a task's status |
| `tasktracker task delete ID` | Delete a task and its subtasks; `--yes` |
| `tasktracker subtask add TASK_ID TITLE` | Add a checklist item; `--json` |
| `tasktracker subtask tick ID` / `untick ID` | Tick or untick an item |
| `tasktracker subtask edit ID --title T` | Rename an item |
| `tasktracker subtask delete ID` | Remove an item |
| `tasktracker key new` | Create the backup keypair and print the config; `--out` |
| `tasktracker backup` | Write an encrypted backup; `--dir`, `--recipient` |
| `tasktracker restore [FILE]` | Replace the database with a backup; `--identity`, `--yes` |
| `tasktracker update` | Install the latest release; `--check`, `--force` |
| `tasktracker version` | Print the version |

Due dates are `YYYY-MM-DD`, `today` or `tomorrow`. Every command also takes
`--db` and `--config` to point at a different database or config file.

With no filter, `task list --json` prints the tree as one object with two
lists, `areas` and `projects`. Each area carries its own `areas` and
`projects`, each project its `tasks`, and each task its `subtasks`. Every
list is present and is `[]` rather than `null` when empty, so a script can
iterate them without checks. With `--project`, `--status` or `--due` it
prints a flat array of tasks, and every other list command prints a plain
array.

## Development

```bash
make test
make build
```

Go 1.26, Cobra, Bubble Tea v2, huh, modernc SQLite, age. Every commit is
reviewed by [roborev](https://github.com/kenn-io/roborev). Releases are cut
by tagging: `git tag -a v0.1.0 -m "..." && git push origin v0.1.0`.

## License

[MIT](LICENSE)
