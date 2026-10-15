---
title: Control tasks and read their history
summary: Start, stop, restart, run now, cancel and delete tasks, act on all periodic tasks at once, and understand attempts, retries and failures.
kind: guide
order: 30
tags: [scheduler, control, history, retries, troubleshooting]
requires: scheduler:read
---
Everyone with `scheduler:read` can see tasks and their history. The actions
on a task need more: `tasks:control` to start, stop, restart, run and cancel,
`tasks:manage` to edit, and `tasks:delete` to delete. A button you may not
use is not shown.

## Actions on a task

The buttons are at the end of each row on the **Scheduled tasks** page.

| Action | For | What it does |
|---|---|---|
| **Start** | stopped periodic tasks | enables the task; the next run is computed from now |
| **Stop** | enabled periodic tasks | the task no longer runs until started |
| **Restart** | enabled periodic tasks | stop and start: the next run is computed from now |
| **Run now** | any task not cancelled | queues one attempt immediately; the schedule does not change |
| **Run again** | completed one-shot tasks | the same as **Run now** |
| **Cancel task** | delayed or wait-for-result tasks that have not finished | the task will not run; queued attempts are cancelled. Cannot be undone |
| **Delete task** | any task | queued attempts are cancelled; a running attempt finishes and is recorded; the history is deleted with the task |

**Run now** is refused with "run_in_progress" while an attempt of the task is
queued or running, and refused when the task's type is unavailable or its
payload invalid. After **Run now** the portal opens the run and follows it.

## All periodic tasks at once

Above the list, **Start all**, **Stop all** and **Restart all** act on every
periodic task you can see. One-shot tasks are never touched. The message
afterwards tells how many tasks changed: **Start all** counts only tasks that
were stopped, **Stop all** only tasks that were enabled. **Restart all**
restarts every periodic task, and this includes stopped ones: they are
started too. If some tasks should stay stopped, restart the others one by
one.

## Filters

Filter the list by name or type (**Search**), **Kind**, **State**, **Type**
and **Validity**. Sort by name, type, state, next run or last update.

## History

Open a task and choose **History**, or use the history button on its row.
Each row is one attempt: status, trigger, attempt number, start and
duration. Badges at the top count succeeded and failed attempts.
**Failed only**, **Status**, **From** and **To** narrow the list. Select an
attempt to see its message and the module's result.

| Status | Meaning |
|---|---|
| Queued | waiting to start (a retry waits here until its time) |
| Running | sent to the module |
| Succeeded | the module reported success |
| Failed | the module reported a failure, refused the call, or could not be reached |
| Timed out | the module did not answer within the task's timeout |
| Skipped | the run was not sent; the message says why |
| Cancelled | the task was cancelled or deleted before the attempt started |

The trigger is **Schedule**, **Manual** (run now or run again, with the user
who started it) or **Catch-up**.

## Retries

An attempt that fails for a reason worth retrying (the module is
unreachable, the attempt timed out, or the module reported a temporary
failure) is followed by another attempt, until the task has used its **Max
retries**. The delay grows: 30 seconds before the second attempt, then 1, 2,
4 and 8 minutes, and at most 10 minutes. If the module reports a permanent
failure, or refuses the call, the scheduler stops at once.

The final attempt sets the task's **Last result**. A one-shot task is then
**Completed**, whatever the result.

## Common failures

| Message | What to do |
|---|---|
| `module unavailable` | the owning module is down or not reachable from the scheduler; it is retried. If it persists, ask your operator |
| `timed out waiting for the module` | the job took longer than the task's timeout; raise **Timeout (seconds)** if the job is slow by nature |
| `refused by module: …` | the module refused the call; not retried. Ask your operator |
| `previous run still in progress` | the run was skipped because the last one had not finished |
| `task type unavailable` | the module unregistered the type; the task waits until it returns |
| `payload no longer matches the task type's schema` | edit the task and correct the payload |

Other messages come from the module itself and describe its own problem.

## The dashboard

**Dashboard** sums up your scope: tasks per state, tasks with an unavailable
type or invalid payload, attempts and failures (failed or timed out) in the
last 24 hours, the next runs due and the failing tasks.

Related: [schedule a periodic task](periodic-tasks.md),
[reference](../reference.md#execution-statuses).
