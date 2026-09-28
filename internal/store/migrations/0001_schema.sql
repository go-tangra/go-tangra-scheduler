-- +goose Up
-- Scheduler schema (data-model.md). Task types are a platform catalog (no tenant,
-- no RLS; owner = the registering module's SPIFFE service name). Tasks and
-- executions carry tenant_id under FORCE RLS (0003). Platform-scoped tasks use
-- the nil uuid as their tenant. Executions have no FK to tasks: history
-- survives task deletion.

CREATE TABLE scheduler_task_types (
  name                text PRIMARY KEY CHECK (name ~ '^[a-z0-9-]{1,63}:[a-z][a-z0-9-]{0,62}$'),
  module              text NOT NULL CHECK (module ~ '^[a-z0-9-]{1,63}$'),
  display_name        text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 200),
  description         text NOT NULL DEFAULT '' CHECK (char_length(description) <= 2000),
  payload_schema      jsonb,
  default_cron        text NOT NULL DEFAULT '',
  default_max_retries int NOT NULL DEFAULT 3 CHECK (default_max_retries BETWEEN 0 AND 10),
  scope               text NOT NULL DEFAULT 'tenant' CHECK (scope IN ('tenant','platform')),
  available           boolean NOT NULL DEFAULT true,
  registered_at       timestamptz NOT NULL DEFAULT now(),
  unregistered_at     timestamptz,
  schema_hash         text NOT NULL DEFAULT '',
  CHECK (split_part(name, ':', 1) = module)
);
CREATE INDEX scheduler_task_types_module ON scheduler_task_types (module);

CREATE TABLE scheduler_tasks (
  id               uuid PRIMARY KEY,
  tenant_id        uuid NOT NULL,
  name             text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
  type_name        text NOT NULL,
  module           text NOT NULL,
  kind             text NOT NULL CHECK (kind IN ('periodic','delayed','wait_result')),
  payload          jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload) = 'object'),
  cron             text NOT NULL DEFAULT '',
  timezone         text NOT NULL DEFAULT 'UTC',
  run_at           timestamptz,
  enabled          boolean NOT NULL DEFAULT true,
  status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active','completed','cancelled')),
  validity         text NOT NULL DEFAULT 'ok' CHECK (validity IN ('ok','type_unavailable','payload_invalid')),
  validity_message text NOT NULL DEFAULT '',
  max_retries      int NOT NULL CHECK (max_retries BETWEEN 0 AND 10),
  timeout_seconds  int NOT NULL DEFAULT 300 CHECK (timeout_seconds >= 1),
  catch_up         boolean NOT NULL DEFAULT false,
  remark           text NOT NULL DEFAULT '' CHECK (char_length(remark) <= 1000),
  next_run_at      timestamptz,
  last_run_at      timestamptz,
  last_status      text NOT NULL DEFAULT '',
  last_message     text NOT NULL DEFAULT '',
  run_count        bigint NOT NULL DEFAULT 0,
  missed_count     bigint NOT NULL DEFAULT 0,
  created_by       text NOT NULL DEFAULT '',
  updated_by       text NOT NULL DEFAULT '',
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  CHECK ((kind = 'periodic') = (cron <> ''))
);
CREATE UNIQUE INDEX scheduler_tasks_name ON scheduler_tasks (tenant_id, lower(name));
CREATE INDEX scheduler_tasks_due ON scheduler_tasks (next_run_at)
  WHERE enabled AND status = 'active' AND next_run_at IS NOT NULL;
CREATE INDEX scheduler_tasks_type ON scheduler_tasks (tenant_id, type_name);
CREATE INDEX scheduler_tasks_type_name ON scheduler_tasks (type_name);
CREATE INDEX scheduler_tasks_updated ON scheduler_tasks (tenant_id, updated_at DESC);

CREATE TABLE scheduler_executions (
  id               uuid PRIMARY KEY,
  tenant_id        uuid NOT NULL,
  task_id          uuid NOT NULL,
  task_name        text NOT NULL DEFAULT '',
  occurrence_id    uuid NOT NULL,
  type_name        text NOT NULL,
  module           text NOT NULL,
  trigger          text NOT NULL CHECK (trigger IN ('schedule','manual','catch_up')),
  triggered_by     text NOT NULL DEFAULT '',
  occurrence_at    timestamptz NOT NULL,
  due_at           timestamptz NOT NULL,
  status           text NOT NULL CHECK (status IN ('queued','running','succeeded','failed','skipped','timed_out','cancelled')),
  attempt          int NOT NULL CHECK (attempt >= 1),
  max_attempts     int NOT NULL CHECK (max_attempts >= 1),
  started_at       timestamptz,
  finished_at      timestamptz,
  duration_ms      bigint NOT NULL DEFAULT 0,
  message          text NOT NULL DEFAULT '',
  result           bytea,
  result_truncated boolean NOT NULL DEFAULT false,
  final            boolean NOT NULL DEFAULT false,
  lease_owner      text NOT NULL DEFAULT '',
  lease_until      timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX scheduler_executions_claim ON scheduler_executions (status, due_at) WHERE status = 'queued';
CREATE INDEX scheduler_executions_lease ON scheduler_executions (lease_until) WHERE status = 'running';
CREATE INDEX scheduler_executions_task ON scheduler_executions (tenant_id, task_id, created_at DESC);
CREATE INDEX scheduler_executions_tenant ON scheduler_executions (tenant_id, created_at DESC);
CREATE INDEX scheduler_executions_created ON scheduler_executions (created_at);
CREATE INDEX scheduler_executions_active ON scheduler_executions (task_id) WHERE status IN ('queued','running');
-- Exactly once: one attempt n of one scheduled occurrence (manual runs excluded).
CREATE UNIQUE INDEX scheduler_executions_occurrence ON scheduler_executions (task_id, occurrence_at, attempt)
  WHERE trigger <> 'manual';

-- +goose Down
DROP TABLE IF EXISTS scheduler_executions;
DROP TABLE IF EXISTS scheduler_tasks;
DROP TABLE IF EXISTS scheduler_task_types;
