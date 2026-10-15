---
title: Scheduler
summary: Run other modules' jobs on a schedule, once after a delay, or right now, with retries, history and live status.
kind: guide
order: 0
tags: [scheduler, tasks, overview]
---
The scheduler decides **when** work runs. The work itself belongs to other
modules: lcm, notification, ipam and others each register the jobs they can
do, and the scheduler asks the owning module to run them at the right time.

## Concepts

- **Task type** — a job a module can run, named `<module>:<action>`, for
  example `lcm:check-expiring-certificates`. The module declares its display
  name, a description, the fields of its **payload**, a suggested schedule and
  a default number of retries. You cannot create types yourself.
- **Task** — one use of a task type in your tenant: a name, a payload (the
  settings the module needs) and when to run. There are three kinds:
  - **Periodic** — repeats on a cron schedule in a time zone.
  - **Delayed** — runs once: now, after a delay, or at a date and time.
  - **Wait for result** — runs once, right away, and the portal follows the
    run live until it finishes.
- **Execution** — one attempt to run a task. A failed attempt that can be
  retried is followed by another attempt, until the task's retries are used
  up. Every attempt is kept in the task's history with its status, message
  and the module's result.

The payload is checked against the type's fields when you save the task and
again before every run. Do not put passwords or other secrets in a payload.

## Pages in the portal

| Page | Shows | Needs |
|---|---|---|
| Scheduled tasks (menu **Scheduler**) | tasks, their schedule, state and last result; history and actions | `scheduler:read` |
| Scheduler dashboard (menu **Dashboard**) | task counts, runs and failures in the last 24 hours, next runs, failing tasks | `scheduler:read` |

Both pages update live. Your role decides what you can do; see
[permissions and roles](reference.md#permissions-and-roles).

## Where to go next

- [Get started](getting-started.md): run a task and watch its result.
- [Schedule a periodic task](guides/periodic-tasks.md)
- [Control tasks and read their history](guides/control-and-history.md)
- [Example: a daily digest of expiring certificates](examples/expiring-certificates.md)
- [Reference](reference.md): fields, states, statuses, errors and limits.
