package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/tx7do/go-crud/entgo/mixin"
)

// TaskExecution records each individual execution of a scheduled task.
type TaskExecution struct {
	ent.Schema
}

func (TaskExecution) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Table:     "scheduler_task_executions",
			Charset:   "utf8mb4",
			Collation: "utf8mb4_bin",
		},
		entsql.WithComments(true),
		schema.Comment("Task execution history"),
	}
}

func (TaskExecution) Fields() []ent.Field {
	return []ent.Field{
		field.String("execution_id").
			Unique().
			NotEmpty().
			Comment("Unique execution ID (UUID)"),

		field.String("task_type").
			NotEmpty().
			Comment("Task type name (e.g., backup:cleanup-old)"),

		field.String("module_id").
			NotEmpty().
			Comment("Module that executed the task"),

		field.String("status").
			NotEmpty().
			Comment("Execution status: success, failed, running"),

		field.String("message").
			Optional().
			Nillable().
			Comment("Execution result or error message"),

		field.Int32("attempt").
			Default(1).
			Comment("Attempt number"),

		field.Int64("duration_ms").
			Default(0).
			Comment("Execution duration in milliseconds"),

		field.Time("started_at").
			Default(time.Now).
			Comment("When execution started"),

		field.Time("finished_at").
			Optional().
			Nillable().
			Comment("When execution finished"),

		field.Uint32("tenant_id").
			Default(0).
			Comment("Tenant context"),
	}
}

func (TaskExecution) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.AutoIncrementId{},
	}
}

func (TaskExecution) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("task_type", "started_at").
			StorageKey("idx_scheduler_exec_task_started"),

		index.Fields("module_id", "started_at").
			StorageKey("idx_scheduler_exec_module_started"),

		index.Fields("status").
			StorageKey("idx_scheduler_exec_status"),
	}
}
