// A small outline for the tests, built so that tree order and due order
// disagree:
//
//   home (area 1)
//     garden (area 2)
//       maintenance (project 2)
//         mow the lawn (task 3, due 2026-11-20, later)
//     house (project 1)
//       paint the hall (task 1, due 2026-09-10, overdue)
//         [x] buy paint (subtask 1)
//         [ ] move furniture (subtask 2)
//       fix the gate (task 2, due 2026-10-09, this week)
//   admin (project 3)
//     email accountant (task 4, undated)
//     old thing (task 5, done)
//
// The stamps are all different, as SQLite's would be.

import type { Outline } from "./api";

export function fixture(): Outline {
  const made = (n: number) => `2026-01-0${n}T00:00:00Z`;
  return {
    areas: [
      {
        area: { id: 1, name: "home", created_at: made(1) },
        areas: [
          {
            area: { id: 2, name: "garden", parent_id: 1, created_at: made(2) },
            areas: [],
            projects: [
              {
                project: { id: 2, name: "maintenance", state: "active", area_id: 2, created_at: made(4) },
                tasks: [
                  {
                    task: { id: 3, project_id: 2, title: "mow the lawn", status: "todo", due: "2026-11-20", created_at: made(7) },
                    subtasks: [],
                  },
                ],
              },
            ],
          },
        ],
        projects: [
          {
            project: { id: 1, name: "house", state: "active", area_id: 1, created_at: made(3) },
            tasks: [
              {
                task: { id: 1, project_id: 1, title: "paint the hall", status: "todo", due: "2026-09-10", notes: "two coats", created_at: made(5) },
                subtasks: [
                  { id: 1, task_id: 1, title: "buy paint", done: true, created_at: made(5) },
                  { id: 2, task_id: 1, title: "move furniture", done: false, created_at: made(5) },
                ],
              },
              {
                task: { id: 2, project_id: 1, title: "fix the gate", status: "todo", due: "2026-10-09", created_at: made(6) },
                subtasks: [],
              },
            ],
          },
        ],
      },
    ],
    projects: [
      {
        project: { id: 3, name: "admin", state: "active", created_at: made(8) },
        tasks: [
          { task: { id: 4, project_id: 3, title: "email accountant", status: "todo", created_at: made(9) }, subtasks: [] },
          { task: { id: 5, project_id: 3, title: "old thing", status: "done", created_at: made(9) }, subtasks: [] },
        ],
      },
    ],
  };
}
