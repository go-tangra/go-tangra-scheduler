---
title: Schedule a periodic task
summary: Create a task that repeats on a cron schedule in a time zone, check its next runs, and decide what happens to runs missed while the scheduler was down.
kind: guide
order: 20
tags: [scheduler, periodic, cron, timezone, catch-up]
requires: tasks:manage
---
A periodic task runs its type's job on a cron schedule. You need
`tasks:manage` to create and edit tasks.

## Create the task

1. Open **Scheduler** and choose **New task**.
2. Choose the **Task type**. If the type suggests a schedule, the **Cron
   expression** is filled in with it, and **Max retries** with the type's
   default.
3. Keep **Kind** on **Periodic**.
4. Enter or change the **Cron expression** and choose the **Time zone**
   (default `UTC`).
5. Fill in the **Payload** fields. Required fields are marked; fields with a
   default are pre-filled.
6. Choose **Create**.

The task and kind are fixed after creation. Everything else (name, schedule,
time zone, payload, options) can be changed later by opening the task.

## The cron expression

Five fields, separated by spaces:

```text
minute  hour  day-of-month  month  day-of-week
0-59    0-23  1-31          1-12   0-7 (0 and 7 are Sunday)
```

Each field takes `*`, a value, a range `a-b`, a list `a,b,c` or a step
(`*/15`, `8-18/2`). Months and days may be written as names (`JAN`, `MON`).
Seconds are not supported.

| Expression | Runs |
|---|---|
| `0 3 * * *` | every day at 03:00 |
| `*/15 * * * *` | every 15 minutes |
| `0 8 * * 1-5` | weekdays at 08:00 |
| `30 6 1 * *` | on the 1st of every month at 06:30 |

When both day-of-month and day-of-week are restricted, a day matches if
**either** matches: `0 0 13 * 5` runs on every 13th and on every Friday.

Under the field, **Next runs** lists the next five run times in the chosen
time zone. If the expression is wrong, the error names the field. Two runs
must be at least one minute apart (the platform's
`limits_scheduler.min_interval_seconds`, 60 by default); a schedule that
fires more often is refused with "cron_too_frequent".

## Time zones and daylight saving time

Run times are wall-clock times in the task's time zone. When clocks jump
forward, a time inside the skipped hour does not run that day. When clocks
jump back, a time inside the repeated hour runs once.

## Options

| Option | Meaning |
|---|---|
| **Max retries** | 0–10 extra attempts after a failure that can be retried |
| **Timeout (seconds)** | how long one attempt may take; empty means 300 |
| **Enabled** | off creates the task stopped: it does not run until started |
| **Catch up missed runs** | see below |
| **Remark** | free text, up to 1000 characters |

## Missed runs and catch-up

A run that cannot start within a minute of its time (the default grace; for example because
the scheduler was down) counts as **missed**: the task's missed count goes
up and the next run is scheduled from now. With **Catch up missed runs** on,
the scheduler makes **one** catch-up run when it is back, however many runs
were missed; its trigger is shown as **Catch-up**. With it off, missed runs
are only counted.

## When a run is skipped

A due run is recorded as **Skipped**, and nothing is sent to the module, when:

- the previous run of the task is still in progress;
- the task type is unavailable (its module unregistered it);
- the payload no longer matches the type's fields (the module changed them).

A task in the last two cases shows **Type unavailable** or **Payload invalid**
in the list and in its drawer. Edit the payload to fix an invalid one. A type
becomes available again when its module registers it again.

Related: [control and history](control-and-history.md),
[reference](../reference.md#task-fields).
