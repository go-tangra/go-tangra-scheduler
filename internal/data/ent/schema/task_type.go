package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// TaskType holds the schema definition for registered task types.
// Each entry represents a task type that a module has registered
// with the scheduler for remote execution.
type TaskType struct {
	ent.Schema
}

func (TaskType) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Table:     "scheduler_task_types",
			Charset:   "utf8mb4",
			Collation: "utf8mb4_bin",
		},
		entsql.WithComments(true),
		schema.Comment("Registered task types from modules"),
	}
}

func (TaskType) Fields() []ent.Field {
	return []ent.Field{
		field.String("task_type").
			Unique().
			NotEmpty().
			Comment("Globally unique task type name (e.g., paperless:ocr-reindex)"),

		field.String("module_id").
			NotEmpty().
			Comment("Owning module ID (e.g., paperless, deployer)"),

		field.String("display_name").
			Optional().
			Nillable().
			Comment("Human-readable display name"),

		field.String("description").
			Optional().
			Nillable().
			Comment("Description of what this task does"),

		field.String("payload_schema").
			Optional().
			Nillable().
			Comment("JSON Schema for the task payload"),

		field.String("default_cron").
			Optional().
			Nillable().
			Comment("Default cron expression suggestion"),

		field.Int32("default_max_retry").
			Default(3).
			Comment("Default max retries"),

		field.Time("registered_at").
			Default(time.Now).
			Comment("When this task type was last registered"),
	}
}

func (TaskType) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("module_id").
			StorageKey("idx_scheduler_task_type_module"),
	}
}
