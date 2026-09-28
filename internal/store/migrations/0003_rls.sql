-- +goose Up
-- Per-tenant row-level security on every tenant-owned scheduler table.
-- scheduler_app is NOBYPASSRLS (created by the stack's init-db, not here);
-- every statement runs with app.tenant_id set to the caller's tenant. The
-- engine, the audit writer and platform-admin reads (only after the caller has
-- been authorised) set app.system='on' (with app.tenant_id pinned to the nil
-- uuid so the cast stays valid). Platform-scoped tasks live in the nil tenant,
-- which no caller's token ever carries.
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['scheduler_tasks','scheduler_executions','scheduler_audit_events']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format($p$CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_setting('app.tenant_id', true)::uuid OR current_setting('app.system', true) = 'on') WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid OR current_setting('app.system', true) = 'on')$p$, t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO scheduler_app', t);
  END LOOP;
END $$;
-- +goose StatementEnd

-- The task-type catalog is platform data read by every tenant: no RLS, and the
-- app role may never delete a type (types are only ever made unavailable).
GRANT SELECT, INSERT, UPDATE ON scheduler_task_types TO scheduler_app;

-- +goose Down
REVOKE ALL ON scheduler_task_types FROM scheduler_app;
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['scheduler_tasks','scheduler_executions','scheduler_audit_events']
  LOOP
    EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
    EXECUTE format('ALTER TABLE %I DISABLE ROW LEVEL SECURITY', t);
  END LOOP;
END $$;
-- +goose StatementEnd
