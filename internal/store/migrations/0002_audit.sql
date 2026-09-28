-- +goose Up
-- Append-only audit hypertable partitioned by its timestamp. A hypertable carries
-- no PRIMARY KEY excluding the partition column, so ids are app-generated uuid v7.
-- Detail is redacted jsonb: never payload values, results, e-mail addresses,
-- secrets or tokens (contracts/audit-events.md).

CREATE TABLE scheduler_audit_events (
  id           uuid NOT NULL,
  tenant_id    uuid NOT NULL,
  at           timestamptz NOT NULL DEFAULT now(),
  actor_kind   text NOT NULL DEFAULT '',
  actor_id     text NOT NULL DEFAULT '',
  action       text NOT NULL DEFAULT '',
  subject_kind text NOT NULL DEFAULT '',
  subject_id   text NOT NULL DEFAULT '',
  outcome      text NOT NULL DEFAULT '',
  reason       text NOT NULL DEFAULT '',
  detail       jsonb
);
SELECT create_hypertable('scheduler_audit_events', 'at', if_not_exists => TRUE, migrate_data => TRUE);
CREATE INDEX scheduler_audit_tenant ON scheduler_audit_events (tenant_id, at DESC);

-- +goose Down
DROP TABLE IF EXISTS scheduler_audit_events;
