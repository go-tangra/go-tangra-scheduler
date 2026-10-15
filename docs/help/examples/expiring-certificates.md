---
title: "Example: a daily digest of expiring certificates"
summary: Schedule lcm's expiring-certificates check every weekday morning in your time zone, with a 30-day horizon and two recipients.
kind: example
order: 50
tags: [scheduler, example, periodic, lcm, certificates]
requires: tasks:manage
---
**Goal.** Every weekday at 08:00 Sofia time, the PKI team and the on-call
mailbox should get an email listing the certificates that expire within the
next 30 days. If lcm is briefly unreachable, the digest should still go out.

**Plan.** One periodic task of lcm's type **Notify expiring certificates**
(`lcm:check-expiring-certificates`).

## 1. The task

In **Scheduler → New task**:

| Field | Value |
|---|---|
| Task type | Notify expiring certificates (lcm) |
| Name | `Expiring certificates digest` |
| Kind | Periodic |
| Cron expression | `0 8 * * 1-5` (the type suggests `0 8 * * *`, every day) |
| Time zone | `Europe/Sofia` |
| daysBeforeExpiry | `30` (default 7, at most 365) |
| recipients | `pki@example.com`, `oncall@example.com` (1 to 20 addresses) |
| Max retries | `3` (the type suggests 1) |
| Catch up missed runs | on |

**Next runs** lists the next five weekdays at 08:00 in Sofia. Choose
**Create**.

The same through the API, with `POST /api/scheduler/v1/tasks`:

```json
{
  "name": "Expiring certificates digest",
  "type_name": "lcm:check-expiring-certificates",
  "kind": "periodic",
  "cron": "0 8 * * 1-5",
  "timezone": "Europe/Sofia",
  "payload": {
    "daysBeforeExpiry": 30,
    "recipients": ["pki@example.com", "oncall@example.com"]
  },
  "max_retries": 3,
  "catch_up": true
}
```

The answer is `201` with the task, including its `next_run_at`.

## 2. Try it now

Choose **Run now** on the row. This sends the real digest to both
recipients (when nothing expires, lcm sends no email). The portal follows the attempt; on success the message from lcm
reads, for example, "Found 3 certificate(s) expiring within 30 days;
notified 2 recipient(s)", or "No certificates expiring within 30 days". A
manual run does not change the schedule.

## 3. What happens on a bad morning

- **lcm is down at 08:00.** Attempt 1 fails with `module unavailable`.
  Attempt 2 follows 30 seconds later, attempt 3 a minute after that, attempt
  4 two minutes later. With `max_retries: 3` that is the last one; the
  task's **Last result** shows the outcome of the final attempt.
- **The scheduler itself is down from 07:30 to 09:00.** The 08:00 run is
  missed. Because **Catch up missed runs** is on, one catch-up run starts
  when the scheduler is back; its trigger is **Catch-up**.
- **A recipient address is mistyped.** Saving is refused and the field is
  highlighted: the payload must match the type's fields.

## Variations

- **Weekly instead of daily:** `0 8 * * MON`.
- **Pause for a holiday week:** **Stop** the task, and **Start** it again
  afterwards. The next run is computed from the moment you start it.

Related: [schedule a periodic task](../guides/periodic-tasks.md),
[control and history](../guides/control-and-history.md).
