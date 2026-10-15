---
title: Get started
summary: Create a wait-for-result task that sends a test email, and follow the run live until it finishes.
kind: guide
order: 10
tags: [scheduler, getting-started, wait-result]
requires: tasks:manage
---
This walk-through runs one task right away and shows its result. It uses the
**Send test email** type of the notification module; if your platform does
not have that module, pick any other type in step 2 and fill in its fields.

You need `tasks:manage` to create the task (the *Scheduler operator* and
*Scheduler administrator* roles have it) and `scheduler:read` to see it.

## 1. Start a new task

1. Open **Scheduler** in the menu. The **Scheduled tasks** page lists the
   tasks of your tenant.
2. Choose **New task**.

## 2. Choose the type and kind

1. In **Task type**, choose **Send test email (notification)**. The name is
   filled in from the type and the type's description appears below.
2. In **Kind**, choose **Wait for result**.

A wait-for-result task always runs immediately, so there is no time to pick.

## 3. Fill in the payload

The **Payload** section shows the fields the type asks for. For this type
only **recipient** is required: enter your email address. Leave the other
fields empty to use the module's defaults.

Leave **Options** as they are: **Max retries** comes from the type and the
timeout defaults to 300 seconds.

## 4. Create and follow the run

Choose **Create**. The scheduler stores the task and queues its first attempt
at once, and the portal opens the run. It moves from **Queued** to
**Running** to **Succeeded** without a page refresh. The drawer shows the
module's message (for this type, "Test email sent to …") and its result.

If the attempt fails and retries are left, the next attempt is queued after
30 seconds and the drawer keeps following it.

## 5. Find it again

The task stays in the list with state **Completed**. Open it and choose the
**History** tab to see every attempt. **Run again** (the play button on the
row) sends another test email without creating a new task.

## Next steps

- Run something every day: [schedule a periodic task](guides/periodic-tasks.md).
- Stop, start and cancel tasks, and read failures:
  [control and history](guides/control-and-history.md).
