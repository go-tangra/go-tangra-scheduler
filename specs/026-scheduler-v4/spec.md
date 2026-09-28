# Feature Specification: Scheduler Module for v4

**Feature Branch**: `026-scheduler-v4`

**Created**: 2026-09-28

**Status**: Draft (clarified 2026-09-28)

**Spans**: new v4 scheduler module (API + UI remote), go-tangra-ipam-v4, go-tangra-lcm-v4, go-tangra-notification-v4 (task types they execute), go-tangra-auth (scheduler permissions and module roles), go-tangra-portal-v4 (UI remote, gateway route), go-tangra-docker (stack, mesh policies)

**Input**: User description: "V3 have following module /home/jadmin/projects/go-tangra/go-tangra-scheduler i like the same with same functionality but for V4"

**Decisions taken with the user (2026-09-28)**:

1. **Tenancy**: tasks belong to a tenant and run for it. Tenant
   administrators manage their own tenant's tasks, and platform
   administrators see all tenants' tasks. A task type may declare itself
   platform-scoped; only platform administrators create tasks of such a
   type, and those tasks run without a tenant (FR-025–FR-027).
2. **Built-in module jobs stay internal**: the modules' existing internal
   periodic loops are unchanged. The scheduler adds the ported v3 task types
   and any new types that modules offer (FR-028).

## Context

### What v3 provides

The v3 scheduler is a central, UI-driven job scheduler. It does no work itself.
It decides **when** something runs and asks the module that owns the work to
run it:

- **Task types come from modules.** On startup a module registers the task
  types it can execute: a unique name (`{module}:{action}`), display name,
  description, a JSON Schema for the task's payload, a suggested cron
  expression and a default retry count. The scheduler stores them and lists
  them in the UI. Re-registering is idempotent. A module may unregister all its
  types. Tasks that already exist are not removed.
- **Administrators create tasks** from those types in the "Scheduler → Tasks"
  page. A task has a type, a payload (JSON), an enable switch, a remark and
  options (max retries, timeout, plus lower-level queue options). A task can
  be one of three kinds:
  - **Periodic**: runs on a 5-field cron expression.
  - **Delayed**: runs once, now or after a delay / at a given time.
  - **Wait-for-result**: runs once, and the caller waits until the run
    finishes.

  Picking a type pre-fills the type's suggested cron expression and retry
  count.
- **Execution happens in the owning module.** When a task fires, the
  scheduler calls the module's `ExecuteTask` operation (shared contract in
  go-tangra-common) with an execution id, the type, the payload, the attempt
  and max attempts, the tenant and the scheduled time. The module answers with
  success, a message, optional result data and a "permanent failure — do not
  retry" flag. A failed run is retried up to the task's max retries with
  backoff. A permanent failure stops retries. If the module is unreachable,
  the run is recorded as failed and retried.
- **Execution history.** Every run is recorded with its execution id, type,
  module, status (success/failed), message, attempt, duration and
  start/finish times. The task shows its last run time, status and message
  and its run count. The UI has a history drawer with success/failure counts
  and a "failed only" filter.
- **Control.** Start, stop or restart one task, or start, stop or restart
  all tasks. Stop and restart affect only periodic tasks, because one-shot
  tasks cannot be stopped.
- **Permissions and menus.** The permissions are `scheduler.task.view`,
  `.create`, `.update`, `.delete` and `.control`. The built-in roles are
  Scheduler Administrator (all five), Scheduler Operator (all except delete)
  and Scheduler Viewer (view only).
- **Backup.** Task types, tasks and execution history can be exported and
  imported.
- **Task types in use**:
  - ipam `ipam:scan-network` ("Scan network"): one subnet by id or CIDR, or
    all subnets, with optional SNMP discovery and DNS sync. Default cron
    `0 3 * * *`, 2 retries.
  - lcm `lcm:check-expiring-certificates` ("Notify expiring certificates"):
    emails LCM and platform admins about issued certificates expiring within
    `daysBeforeExpiry` (default 7). Default cron `0 8 * * *`, 1 retry.
  - notification `notification:send-test-email`: a required `recipient`, with
    optional subject, body and channel. No default cron, 1 retry.
  - backup `backup:full-platform`, `backup:cleanup-old`,
    `backup:validate-all`.

v3 also has shortcomings that this feature must not carry over:

- Every request ran with system privileges. The task list was not isolated
  per tenant, and the permission codes were declared but never enforced.
- The scheduler forwarded no tenant, so every run was effectively
  platform-wide.
- A module registered types under whatever module id it put in the request.
  The only check was an allow-list of caller certificates, so any allowed
  module could register or remove another module's types.
- The payload schema was stored but never used. The UI took raw JSON, and
  payloads were not validated.
- Only one task per type per tenant was allowed.
- One-shot tasks re-fired on every "start all" and scheduler restart.
- Result data was discarded, and the attempt number was not recorded.
- The metrics collector counted nothing.
- A "wait-for-result" task blocked the scheduler's own request until the run
  finished.

### The v4 gap

v4 has no scheduler. v4 modules communicate over the SPIFFE mTLS mesh with
per-module policy files. Tenants are UUIDs. Permissions and module roles are
registered in auth (feature 019), and UIs are federated remotes in the portal
shell. Several v4 modules run fixed, built-in periodic jobs: ipam host-sync
and scan execution, lcm certificate auto-renewal, the asset lifecycle sweep,
the notification send worker and the inventory upgrade policy. None of these
jobs can be scheduled, run on demand from a central place, or watched in one
history view. For the v3 task types:

- **ipam-v4** can start scans on demand (`/subnets/{id}/scan`, `/ip-scans`)
  but cannot run them on a schedule.
- **lcm-v4** auto-renews certificates but does not email anyone about
  expiring certificates.
- **notification-v4** can test a channel on demand (`/channels/{id}/test`)
  but cannot do so on a schedule.
- **backup** has no v4 module.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Modules offer task types; administrators schedule periodic tasks with validated payloads (Priority: P1)

After the stack starts, the ipam, lcm and notification modules have
registered their task types. A tenant administrator opens Scheduler → Tasks
and chooses "New task". They pick "Scan network (ipam)" and see its
description, a form generated from the type's payload schema (subnet, CIDR,
"all subnets", SNMP, DNS sync) and the suggested schedule "every day at
03:00", which they can change. They save the task enabled. The list shows
the task with its type, owning module, schedule in plain words, next run
time and enabled state.

**Why this priority**: This is the core of the module. Without registered
types and scheduled tasks, nothing else has value.

**Independent Test**: A test module registers a type with a payload schema.
A task created from that type with a valid payload is accepted and fires at
the next cron occurrence. An invalid payload or an invalid cron expression is
refused with a message naming the field.

**Acceptance Scenarios**:

1. **Given** a module on the mesh, **When** it registers its task types,
   **Then** the scheduler stores them with the module as owner. A type name
   not prefixed with the caller's own module id is refused, and so is a type
   name owned by another module.
2. **Given** registered types, **When** an administrator opens "New task",
   **Then** they can choose among all currently available types. Each type
   shows its display name, description and owning module. The payload form is
   generated from the type's schema, with a raw-JSON editor as a fallback.
3. **Given** a payload that does not match the type's schema (a required
   field is missing, a value has the wrong type or is out of range), **When**
   the task is saved, **Then** it is refused, naming the offending field.
4. **Given** an invalid cron expression, **When** the task is saved, **Then**
   it is refused. A valid expression shows its next five run times before the
   task is saved.
5. **Given** an enabled periodic task, **When** its cron time arrives,
   **Then** the owning module receives exactly one execution request. The
   request carries the task's tenant, payload, attempt and scheduled time.
6. **Given** two tasks of the same type with different payloads (for example,
   scan subnet A nightly and subnet B hourly), **Then** both can exist and run
   independently.
7. Editing, enabling, disabling and deleting tasks is available to users with
   the matching permission. Each change takes effect for the next occurrence
   without a restart and is audited.

---

### User Story 2 - Execution history with status, result and retries (Priority: P1)

The next morning the administrator opens the scan task's history. They see
each run with its start time, duration, attempt number, status and the
module's message ("queued 12 scan(s), skipped 1"). One run shows "failed,
attempt 1/3 — module unavailable" followed by "succeeded, attempt 2/3". They
filter to failed runs only and open one run to see its full message and
result data.

**Why this priority**: Unattended jobs are only trustworthy when their
outcome is visible. This matches v3's history drawer and last-run fields.

**Independent Test**: A test module fails the first attempt with a retryable
error and succeeds on the second. The history shows two attempts of one
occurrence with the correct statuses and messages. A permanent failure shows
a single attempt and no retry.

**Acceptance Scenarios**:

1. **Given** any run, **Then** it is recorded with execution id, task, type,
   owning module, tenant, scheduled time, start and finish times, duration,
   attempt number and max attempts, status, message and result data (size
   limited).
2. **Given** a retryable failure (an error response, the module unreachable,
   or a timeout), **When** attempts remain, **Then** the run is retried with
   increasing delays. Each attempt is recorded, and the final status is
   reported for the occurrence.
3. **Given** a module response marked permanent failure, **Then** no retry
   happens and the reason is shown.
4. **Given** a task, **Then** the list shows its last run time, last status,
   last message, next run time and total run count.
5. History can be filtered by status and time range and is paginated
   server-side.
6. History is kept for a configurable retention period (default 90 days) and
   then removed automatically.

---

### User Story 3 - One-shot delayed and wait-for-result tasks (Priority: P2)

An operator creates a one-shot "Send test email" task: to run in 10
minutes, and another to run at 22:00 tonight. They also run a
"wait-for-result" task and watch it progress from queued to running to
succeeded. When the run finishes, the page shows the result the module
returned.

**Why this priority**: This is v3 functionality, but it is secondary to
periodic scheduling.

**Independent Test**: A delayed task fires once at the requested time and
never again, including after a scheduler restart. A wait-for-result task
shows its live status and, when it finishes, the result data.

**Acceptance Scenarios**:

1. **Given** a delayed task with a delay or an absolute time (in the past or
   empty meaning "now"), **Then** it runs exactly once, and afterwards it
   shows as completed.
2. **Given** a completed one-shot task, **When** the scheduler restarts or
   "start all" is used, **Then** the task does not run again. It runs again
   only through an explicit "run again" action.
3. **Given** a wait-for-result task, **When** it is started, **Then** the
   request returns immediately with the execution id. The UI follows the
   run's status live until it finishes and then shows the message and result
   data. The request itself never blocks until the run completes.
4. A one-shot task that has not yet fired can be cancelled.

---

### User Story 4 - Control: start, stop, restart, run now (Priority: P2)

Before maintenance on the lab network, an operator stops the nightly scan
task and later starts it again. After a configuration change they use
"Restart all". To check a fix, they use "Run now" on the lcm expiry task
without waiting for 08:00.

**Why this priority**: v3 parity for day-to-day operation.

**Independent Test**: A stopped periodic task does not fire at its next
occurrence. Once started, it fires at the next occurrence after that. "Run
now" produces one immediate run recorded as manual. "Stop all" and "Start
all" affect only tasks in the caller's scope.

**Acceptance Scenarios**:

1. **Given** a periodic task, **When** it is stopped, **Then** it no longer
   fires, and its state shows as stopped (same as disabled) until it is
   started.
2. **Given** a task, **When** "Run now" is used, **Then** one run starts
   immediately. It is recorded with the triggering user, and it does not
   change the task's schedule.
3. **Given** "Stop all", "Start all" or "Restart all", **Then** only the tasks
   in the caller's scope (their tenant, or all tenants for a platform
   administrator) are affected. The response reports how many tasks were
   affected.
4. Every control action is audited (who, which tasks, which action).

---

### User Story 5 - The v3 task types are available in v4 (Priority: P2)

The ipam, lcm and notification v4 modules offer the same task types v3 had:
"Scan network", "Notify expiring certificates" and "Send test email". The
payloads and defaults are the same, adapted to v4 identifiers (UUIDs).

**Why this priority**: "Same functionality" includes the jobs people actually
scheduled in v3.

**Independent Test**: In the running stack, each type appears in the
scheduler. A task of each type runs and produces the v3 outcome:

- **Scan network**: scans are queued in ipam and visible under IP scans.
- **Notify expiring certificates**: an email listing the certificates that
  expire within N days reaches the configured recipients (Mailpit in the dev
  stack).
- **Send test email**: the message arrives through the chosen or default
  email channel.

**Acceptance Scenarios**:

1. **Scan network** (ipam): the payload is a subnet id, a CIDR or "all", plus
   options to enable SNMP discovery and DNS sync. The run queues scans for the
   task's tenant only. Subnets that are already being scanned or cannot be
   scanned are counted as skipped, not failed. The message reports queued,
   skipped and failed counts.
2. **Notify expiring certificates** (lcm): the payload is `daysBeforeExpiry`
   (1–365, default 7). The run finds the tenant's active certificates that
   expire within that window and notifies the tenant's LCM administrators
   through the notification module. With nothing expiring, the run succeeds
   with "No certificates expiring within N days".
3. **Send test email** (notification): the payload is a required recipient
   address, plus an optional subject, body and channel. The run sends through
   the tenant's chosen or default email channel. An invalid recipient is a
   permanent failure.
4. Each module executes only for the tenant named in the request and refuses
   execution requests from any caller other than the scheduler.

---

### User Story 6 - Overview and operational metrics (Priority: P3)

A platform administrator sees a scheduler overview with the following:

- task counts by state (enabled, stopped, completed, type unavailable)
- runs and failures in the last 24 hours
- the next runs due
- tasks whose last run failed

The same figures are exposed to platform monitoring.

**Why this priority**: This is useful, but the product works without it.
(v3's metrics were effectively empty.)

**Independent Test**: After a mix of successful and failed runs, the
overview counts match the recorded history. The monitoring figures show the
same totals.

**Acceptance Scenarios**:

1. The overview shows the figures above for the viewer's scope.
2. Monitoring exposes these counts:
   - runs by type, module and status
   - run duration
   - retries
   - missed and skipped occurrences
   - registered types per module
3. Monitoring figures carry no tenant-identifying payload data.

### Edge Cases

- **Module offline or restarting**: the attempt fails with "module
  unavailable" and is retried with backoff. When attempts are exhausted, the
  occurrence is failed. The task keeps its schedule, and the next occurrence
  runs normally.
- **Module removed or type no longer registered**: its types are marked
  unavailable, not deleted. Tasks of those types are kept, shown as "type
  unavailable", and not run. Each skipped occurrence is recorded as skipped
  with that reason. When the module registers again, the tasks run again.
  Ordinary module restarts do not remove types.
- **Explicit unregistration of a type that has tasks**: allowed. It follows
  the same "type unavailable" behaviour. Deleting such tasks is an
  administrator's decision.
- **Payload schema changes on re-registration**: existing tasks are
  re-validated. Tasks that no longer validate are flagged "payload invalid",
  are not run (each skipped occurrence is recorded with the validation error),
  and can be fixed by editing.
- **Duplicate type names**: a type name registered by another module is
  refused. Re-registration by the owning module updates the type.
- **Overlapping runs**: if the previous run of the same task is still in
  progress when the next occurrence is due, the new occurrence is skipped and
  recorded as skipped ("previous run still in progress").
- **Missed occurrences during scheduler downtime**: they are not replayed
  one by one. At most one catch-up run happens after startup, and only if the
  task allows catch-up (default: no catch-up). Missed occurrences are counted
  and shown.
- **Time zones and DST**: each periodic task has a time zone (default UTC).
  A local time that does not exist on a DST change day is skipped. A local
  time that occurs twice runs once.
- **Long-running work**: each attempt is bounded by the task's timeout
  (default 5 minutes, platform maximum configurable). A module whose work
  takes longer queues it and returns promptly (as ipam scans do). The
  execution records the module's message. The scheduler does not track the
  queued work.
- **Multiple scheduler instances**: each occurrence is executed exactly once,
  whichever instance holds it. If an instance crashes mid-run, the attempt is
  recovered (it times out and is retried) and is never lost silently.
- **Tenant deleted or disabled**: its tasks stop running and are removed with
  the tenant's data.
- **Task deleted while running**: the in-flight run finishes and is recorded.
  No further runs happen.
- **Oversized payload or result**: a payload above the limit is refused at
  save time. Result data above the limit is truncated and marked as
  truncated.
- **Clock skew between scheduler and module**: the module receives the
  intended scheduled time, not the dispatch time.

## Requirements *(mandatory)*

### Functional Requirements

**Task types**

- **FR-001**: Modules MUST be able to register, update and unregister the
  task types they execute. Each type has a name, display name, description,
  payload schema (JSON Schema), suggested cron expression, default max
  retries and scope (tenant, the default, or platform; FR-027). Registration
  is idempotent.
- **FR-002**: The owner of a registered type MUST be the calling module's
  mesh identity, not a value supplied in the request. A type name MUST start
  with the owner's module id followed by `:`.
- **FR-003**: Types MUST persist across scheduler restarts. Types of a module
  that has not re-registered MUST remain, and be shown as unavailable only
  after explicit unregistration or removal of the module.
- **FR-004**: Users with view permission MUST be able to list the available
  task types with their descriptions, schemas and defaults.

**Tasks**

- **FR-005**: Authorised users MUST be able to create, view, edit, enable,
  disable and delete tasks. A task has a name, type, kind, payload, schedule
  or run time, time zone, enabled state, remark and options: max retries,
  timeout, and whether catch-up runs are allowed.
- **FR-006**: Task kinds MUST include periodic (cron), delayed (one-shot at a
  delay or time) and wait-for-result (one-shot whose result the user follows
  live).
- **FR-007**: Cron expressions MUST use the standard 5-field format. Invalid
  expressions MUST be refused, and the next run times MUST be previewable.
- **FR-008**: A payload MUST be validated against its type's schema when the
  task is saved and before each run. A type without a schema accepts any
  JSON object. Payloads MUST be limited in size (default 64 KiB).
- **FR-009**: Several tasks of the same type MUST be allowed in a scope. Task
  names MUST be unique within a scope.
- **FR-010**: Creating a task from a type MUST pre-fill the type's suggested
  cron expression and default max retries.

**Execution**

- **FR-011**: When a task is due, the scheduler MUST send the owning module
  one execution request. The request carries:
  - a unique execution id
  - the type and the payload
  - the attempt number and max attempts
  - the task's tenant (none for platform-scoped tasks, FR-027)
  - the intended scheduled time
- **FR-012**: The scheduler MUST honour the module's response: success,
  message, result data and permanent failure.
- **FR-013**: Retryable failures MUST be retried up to the task's max retries
  with increasing delays. Retryable failures are:
  - an error response without the permanent flag
  - the module unreachable
  - a timeout
- **FR-014**: Each occurrence MUST run at most once across all scheduler
  instances. Overlapping runs of the same task MUST be skipped and recorded.
- **FR-015**: One-shot tasks MUST run exactly once unless explicitly re-run.
  Restarts and "start all" MUST NOT re-fire completed one-shot tasks.
- **FR-016**: Missed occurrences during downtime MUST NOT be replayed
  individually. At most one catch-up run happens when the task allows it.

**History and control**

- **FR-017**: Every attempt MUST be recorded in the execution history with
  these fields:
  - task, type, module, tenant
  - scheduled time, start time and finish time
  - duration, attempt and max attempts
  - status: queued, running, succeeded, failed, skipped, timed out or
    cancelled
  - message and result data (size limited)
  - trigger: schedule, manual or catch-up, with the triggering user for
    manual runs

  Each task MUST show its last run time, last status, last message, next run
  time and run count.
- **FR-018**: History MUST be filterable by status and time range, paginated
  server-side, and pruned after a configurable retention period (default 90
  days).
- **FR-019**: Authorised users MUST be able to start, stop and restart a
  single task, "run now" any task, re-run a completed one-shot task, cancel a
  one-shot task that has not fired, and start, stop or restart all tasks in
  their scope. Bulk actions report how many tasks they affected.
- **FR-020**: Wait-for-result runs MUST return an execution id immediately,
  and the UI MUST follow the run's status live until it finishes.

**Ported task types**

- **FR-021**: ipam MUST offer "Scan network", lcm MUST offer "Notify expiring
  certificates" and notification MUST offer "Send test email". Each has the
  v3 payload fields, defaults and outcomes described in User Story 5. All
  three are limited to the requesting tenant.

**UI, overview and backup**

- **FR-022**: The scheduler UI MUST be a portal module with these pages:
  - task list with state, next run and last result
  - task create/edit form generated from the payload schema, with a JSON
    fallback
  - task detail with history
  - history drawer with a failed-only filter
  - control actions

  Actions the user's permissions do not allow MUST be hidden.
- **FR-023**: An overview (User Story 6) and monitoring metrics MUST be
  provided.
- **FR-024**: Tasks, task types and history MUST be included in the v4 module
  backup export/import, like other v4 modules.

**Tenancy and scope** (decided with the user, 2026-09-28)

- **FR-025**: A task MUST belong to one tenant and run for that tenant. The
  tenant's identifier is sent in every execution request. Tenant
  administrators (users with scheduler permissions in a tenant) MUST see and
  manage only their own tenant's tasks and history.
- **FR-026**: Platform administrators MUST be able to see and manage the
  tasks and history of all tenants.
- **FR-027**: A task type MUST be able to declare itself platform-scoped when
  it is registered. Only platform administrators can create, edit or control
  tasks of a platform-scoped type. Those tasks belong to no tenant, and their
  execution requests carry no tenant. Tenant administrators do not see
  platform-scoped types or tasks. A type that is not platform-scoped (the
  default) is tenant-scoped.

**Built-in module jobs** (decided with the user, 2026-09-28)

- **FR-028**: The periodic jobs v4 modules already run internally MUST stay
  internal and unchanged:
  - ipam host-sync poller and scan executor
  - lcm certificate auto-renewal
  - asset lifecycle sweep
  - notification send worker
  - inventory upgrade policy

  The scheduler adds the ported v3 task types (FR-021) and any new types that
  modules choose to offer. It does not replace, drive or monitor these
  internal loops.

### Security Requirements *(mandatory — Constitution: Development Workflow)*

- **Trust boundaries crossed**:
  - browser → gateway → scheduler (user API)
  - module → scheduler (type registration, service-to-service over the mesh)
  - scheduler → module (execution requests over the mesh)
  - scheduler → auth (permission registration)
- **Data classification**: internal operational data. Payloads are
  configuration, such as subnet ids, email addresses and day counts. They may
  contain personal data (recipient email addresses) but MUST NOT contain
  secrets. Credentials are referenced, for example as Warden secret ids, and
  never embedded.
- **Authentication/Authorization**:
  - User calls are authenticated at the gateway and authorised by scheduler
    permissions in the user's tenant.
  - Registration calls are authenticated by the caller's mesh identity and
    allowed by the scheduler's policy.
  - Execution calls are authenticated by the scheduler's mesh identity and
    allowed only by each executing module's policy.
- **Threat scenarios**:
  - a module registering or hijacking another module's types
  - a forged or replayed execution request to a module
  - a tenant reading or running another tenant's tasks
  - a malicious payload (oversized, deeply nested, or crafted to abuse the
    executing module)
  - secrets leaking through payloads, results, logs or metrics
  - denial of service by very frequent cron expressions or retry storms
- **SR-001**: Only mesh identities allowed by the scheduler's policy may
  register or unregister types. A caller may only manage types under its own
  module id (FR-002).
- **SR-002**: Modules MUST accept execution requests only from the scheduler's
  mesh identity, enforced by each module's policy. They MUST scope all work to
  the tenant in the request. A request without a tenant is accepted only for
  the module's own platform-scoped types and treat the payload as untrusted input,
  validating it again themselves.
- **SR-003**: Every user operation MUST be limited to the caller's tenant.
  Cross-tenant access and platform-scoped types and tasks are limited to
  platform administrators (FR-026, FR-027). Other callers MUST get "not
  found".
- **SR-004**: Permissions MUST be enforced on the server for every operation:
  view, create, update, delete, control (start, stop, restart, run now,
  cancel, bulk). They are registered in auth together with the built-in
  module roles Scheduler Administrator (all), Scheduler Operator (all except
  delete) and Scheduler Viewer (view).
- **SR-005**: These actions MUST be audited with actor, tenant, task or type,
  action and outcome, but never payload values:
  - task create, update and delete
  - enable and disable
  - every control action
  - type registration and unregistration
  - manual runs
- **SR-006**: Payload, cron frequency and retries MUST be bounded:
  - payload size and nesting depth are limited
  - the minimum interval between runs is at least 1 minute
  - retries are capped (maximum 10)
  - the retry delay grows between attempts
- **SR-007**: Payload values and result data MUST NOT appear in logs or
  metrics. Results are returned only to users who may view the task.

### Key Entities

- **Task type**: a unit of work a module can execute. Attributes: name
  (`module:action`), owning module, display name, description, payload
  schema, suggested cron, default max retries, availability (available or
  unavailable), last registration time, scope (tenant or platform).
- **Task**: a scheduled use of a task type. Attributes:
  - tenant (or platform), name, type, kind (periodic, delayed or
    wait-for-result)
  - payload, cron expression or run time, time zone
  - enabled or stopped state, and completion state for one-shot tasks
  - options: max retries, timeout, catch-up
  - remark, created/updated by and when
  - derived: next run, last run time, last status, last message, run count,
    validity (type unavailable or payload invalid)
- **Execution**: one attempt of one occurrence of a task. Attributes:
  - execution id, task, type, module, tenant
  - trigger (schedule, manual or catch-up) and triggering user
  - scheduled time, start time, finish time, duration
  - attempt and max attempts
  - status, message, result data (truncated flag)
- **Module registration**: which mesh identity owns which types, and when
  they were last registered.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: An administrator can create a scheduled "Scan network" task
  from the UI, with the payload entered through the generated form, in under
  2 minutes.
- **SC-002**: Periodic tasks start within 5 seconds of their scheduled time
  in 99% of occurrences under normal load (up to 1,000 enabled tasks).
- **SC-003**: Across a 24-hour test with two scheduler instances and module
  restarts, every occurrence is executed at most once, and none is lost
  without a recorded status.
- **SC-004**: 100% of runs appear in the history with status, attempt and
  message, and the history of a task loads in under 2 seconds with 10,000
  recorded runs.
- **SC-005**: Invalid payloads and cron expressions are rejected at save time
  in 100% of tested cases, with the offending field named.
- **SC-006**: 0 cross-tenant reads or runs, and 0 foreign type registrations
  succeed in the authorization test suite.
- **SC-007**: All three ported v3 task types produce their v3 outcome end to
  end in the dev stack.
- **SC-008**: 0 payload values or secrets are found in logs, metrics or
  audit entries in an end-to-end leak test.

## Assumptions

- **Cron format**: standard 5-field cron (minute resolution), as in v3.
  Seconds-level scheduling is not needed.
- **"Wait-for-result"**: means a one-shot run whose progress and result the
  user follows. The v3 behaviour of blocking the scheduler request until the
  run finished is not kept.
- **Default time zone and catch-up**: the default time zone is UTC, and
  catch-up is off by default.
- **Retry backoff**: increasing delays (for example 30 s, 1 min, 2 min…,
  capped). The exact curve is a planning decision.
- **Payload size and result retention**: the payload limit is 64 KiB, the
  result data limit is 64 KiB and history is kept for 90 days. All are
  configurable at platform level.
- **Advanced v3 queue options**: v3 exposed low-level options such as queue
  group, unique TTL, deadline and result retention. These are replaced by the
  options in FR-005. Overlap protection replaces unique TTL.
- **Module shutdown**: modules no longer unregister their types on shutdown,
  so restarts do not orphan tasks. Explicit unregistration remains available.
- **Shared contract**: the v3 execution and registration contract (execution
  request/response, type descriptor) keeps its fields in v4. It changes only
  where v4 requires it, for example tenant identifiers become UUIDs. The v4
  contract is published for modules to implement.
- **Lcm recipients**: "Notify expiring certificates" in v4 notifies the
  tenant's users holding the LCM administrator role through the notification
  module, in place of v3's issuer email plus platform admins.
- **Data migration**: migrating v3 scheduler data into v4 is not required.
  v4 starts empty, and the ported types show their suggested defaults.

## Dependencies

- v4 framework: mesh identity, per-module policies, audit and permission
  registration in auth (feature 019 module roles).
- Portal v4: gateway route and federated UI remote with permission-aware
  actions.
- ipam-v4 scan queueing, lcm-v4 certificate listing plus a
  lcm → notification path for emails, and the notification-v4 email channels
  and send operation.
- go-tangra-docker: stack service, mesh policy rules allowing module →
  scheduler registration and scheduler → module execution.

## Out of Scope

- The backup task types (`backup:full-platform`, `backup:cleanup-old`,
  `backup:validate-all`). There is no backup module in v4. They follow when
  one exists, through the same registration contract.
- Running arbitrary scripts or commands in the scheduler. All work is
  executed by the owning module.
- Workflow chaining or dependencies between tasks. Calendar or holiday
  exclusions.
- Tracking the progress of work that a module queues internally (for
  example, individual ipam scans) beyond the module's response.
- Migrating v3 scheduler data.
