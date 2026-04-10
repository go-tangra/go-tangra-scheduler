package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/tx7do/go-crud/entgo/mixin"

	schedulerV1 "github.com/go-tangra/go-tangra-scheduler/gen/go/scheduler/service/v1"
)

// Task holds the schema definition for the Task entity.
type Task struct {
	ent.Schema
}

func (Task) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Table:     "scheduler_tasks",
			Charset:   "utf8mb4",
			Collation: "utf8mb4_bin",
		},
		entsql.WithComments(true),
		schema.Comment("Scheduled tasks"),
	}
}

// Fields of the Task.
func (Task) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("type").
			Comment("Task type").
			NamedValues(
				"Periodic", "PERIODIC",
				"Delay", "DELAY",
				"WaitResult", "WAIT_RESULT",
			).
			Default("PERIODIC").
			Optional().
			Nillable(),

		field.String("type_name").
			Comment("Task execution type name (e.g., paperless:ocr-reindex)").
			Optional().
			Nillable(),

		field.String("module_id").
			Comment("Owning module ID (resolved from task type registry)").
			Optional().
			Nillable(),

		field.String("task_payload").
			Comment("Task data in JSON format").
			SchemaType(map[string]string{
				dialect.MySQL:    "json",
				dialect.Postgres: "jsonb",
			}).
			Optional().
			Nillable(),

		field.String("cron_spec").
			Comment("Cron expression for periodic scheduling").
			Optional().
			Nillable(),

		field.JSON("task_options", &schedulerV1.TaskOption{}).
			Comment("Task execution options").
			Optional(),

		field.Bool("enable").
			Comment("Enable/disable task").
			Default(false).
			Optional().
			Nillable(),

		field.Time("last_run_at").
			Comment("When the task last executed").
			Optional().
			Nillable(),

		field.String("last_run_status").
			Comment("Last execution status: success, failed, running").
			Optional().
			Nillable(),

		field.String("last_run_message").
			Comment("Last execution result or error message").
			Optional().
			Nillable(),

		field.Int32("run_count").
			Comment("Total execution count").
			Default(0),
	}
}

// Mixin of the Task.
func (Task) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.AutoIncrementId{},
		mixin.TimeAt{},
		mixin.OperatorID{},
		mixin.Remark{},
		mixin.TenantID[uint32]{},
	}
}

// Indexes of the Task.
func (Task) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "type_name").
			Unique().
			StorageKey("idx_scheduler_task_tenant_type_name"),

		index.Fields("tenant_id", "type").
			StorageKey("idx_scheduler_task_tenant_type"),

		index.Fields("tenant_id", "enable", "created_at").
			StorageKey("idx_scheduler_task_tenant_enable_created_at"),

		index.Fields("tenant_id", "created_by", "created_at").
			StorageKey("idx_scheduler_task_tenant_created_by_created_at"),

		index.Fields("tenant_id", "created_at").
			StorageKey("idx_scheduler_task_tenant_created_at"),
	}
}
