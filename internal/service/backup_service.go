package service

import (
	"context"
	"fmt"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/tx7do/kratos-bootstrap/bootstrap"
	"google.golang.org/protobuf/types/known/timestamppb"

	entCrud "github.com/tx7do/go-crud/entgo"

	"github.com/go-tangra/go-tangra-common/backup"
	"github.com/go-tangra/go-tangra-common/grpcx"

	backupV1 "github.com/go-tangra/go-tangra-backup/gen/go/backup/service/v1"
	"github.com/go-tangra/go-tangra-scheduler/internal/data/ent"
	"github.com/go-tangra/go-tangra-scheduler/internal/data/ent/task"
	"github.com/go-tangra/go-tangra-scheduler/internal/data/ent/taskexecution"
	"github.com/go-tangra/go-tangra-scheduler/internal/data/ent/tasktype"
)

const (
	backupModule        = "scheduler"
	backupSchemaVersion = 1
)

var backupMigrations = backup.NewMigrationRegistry(backupModule)

type BackupService struct {
	backupV1.UnimplementedBackupServiceServer

	log       *log.Helper
	entClient *entCrud.EntClient[*ent.Client]
}

func NewBackupService(ctx *bootstrap.Context, entClient *entCrud.EntClient[*ent.Client]) *BackupService {
	return &BackupService{
		log:       ctx.NewLoggerHelper("scheduler/service/backup"),
		entClient: entClient,
	}
}

func (s *BackupService) ExportBackup(ctx context.Context, req *backupV1.ExportBackupRequest) (*backupV1.ExportBackupResponse, error) {
	tenantID := grpcx.GetTenantIDFromContext(ctx)
	full := false

	if grpcx.IsPlatformAdmin(ctx) && req.TenantId != nil && *req.TenantId == 0 {
		full = true
		tenantID = 0
	} else if req.TenantId != nil && *req.TenantId != 0 && grpcx.IsPlatformAdmin(ctx) {
		tenantID = *req.TenantId
	}

	client := s.entClient.Client()
	a := backup.NewArchive(backupModule, backupSchemaVersion, tenantID, full)

	// Export task types (global, not tenant-scoped)
	taskTypes, err := client.TaskType.Query().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("export task types: %w", err)
	}
	if err := backup.SetEntities(a, "taskTypes", taskTypes); err != nil {
		return nil, fmt.Errorf("set task types: %w", err)
	}

	// Export tasks
	tQuery := client.Task.Query()
	if !full {
		tQuery = tQuery.Where(task.TenantIDEQ(tenantID))
	}
	tasks, err := tQuery.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("export tasks: %w", err)
	}
	if err := backup.SetEntities(a, "tasks", tasks); err != nil {
		return nil, fmt.Errorf("set tasks: %w", err)
	}

	// Export execution history
	exQuery := client.TaskExecution.Query()
	if !full {
		exQuery = exQuery.Where(taskexecution.TenantIDEQ(tenantID))
	}
	executions, err := exQuery.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("export task executions: %w", err)
	}
	if err := backup.SetEntities(a, "taskExecutions", executions); err != nil {
		return nil, fmt.Errorf("set task executions: %w", err)
	}

	data, err := backup.Pack(a)
	if err != nil {
		return nil, fmt.Errorf("pack backup: %w", err)
	}

	s.log.Infof("exported backup: module=%s tenant=%d full=%v entities=%v",
		backupModule, tenantID, full, a.Manifest.EntityCounts)

	return &backupV1.ExportBackupResponse{
		Data:          data,
		Module:        backupModule,
		Version:       fmt.Sprintf("%d", backupSchemaVersion),
		ExportedAt:    timestamppb.New(a.Manifest.ExportedAt),
		TenantId:      tenantID,
		EntityCounts:  a.Manifest.EntityCounts,
		SchemaVersion: int32(backupSchemaVersion),
	}, nil
}

func (s *BackupService) ImportBackup(ctx context.Context, req *backupV1.ImportBackupRequest) (*backupV1.ImportBackupResponse, error) {
	tenantID := grpcx.GetTenantIDFromContext(ctx)
	isPlatformAdmin := grpcx.IsPlatformAdmin(ctx)
	mode := mapRestoreMode(req.GetMode())

	a, err := backup.Unpack(req.GetData())
	if err != nil {
		return nil, fmt.Errorf("unpack backup: %w", err)
	}

	if err := backup.Validate(a, backupModule, backupSchemaVersion); err != nil {
		return nil, err
	}

	if a.Manifest.FullBackup && !isPlatformAdmin {
		return nil, fmt.Errorf("only platform admins can restore full backups")
	}

	sourceVersion := a.Manifest.SchemaVersion
	applied, err := backupMigrations.RunMigrations(a, backupSchemaVersion)
	if err != nil {
		return nil, fmt.Errorf("migration failed: %w", err)
	}

	if !isPlatformAdmin || !a.Manifest.FullBackup {
		tenantID = grpcx.GetTenantIDFromContext(ctx)
	} else {
		tenantID = 0
	}

	client := s.entClient.Client()
	result := backup.NewRestoreResult(sourceVersion, backupSchemaVersion, applied)

	// Import order: taskTypes → tasks → executions
	s.importTaskTypes(ctx, client, a, mode, result)
	s.importTasks(ctx, client, a, tenantID, a.Manifest.FullBackup, mode, result)
	s.importTaskExecutions(ctx, client, a, tenantID, a.Manifest.FullBackup, mode, result)

	s.log.Infof("imported backup: module=%s tenant=%d migrations=%d results=%d",
		backupModule, tenantID, applied, len(result.Results))

	protoResults := make([]*backupV1.EntityImportResult, len(result.Results))
	for i, r := range result.Results {
		protoResults[i] = &backupV1.EntityImportResult{
			EntityType: r.EntityType,
			Total:      r.Total,
			Created:    r.Created,
			Updated:    r.Updated,
			Skipped:    r.Skipped,
			Failed:     r.Failed,
		}
	}

	return &backupV1.ImportBackupResponse{
		Success:           result.Success,
		Results:           protoResults,
		Warnings:          result.Warnings,
		SourceVersion:     int32(result.SourceVersion),
		TargetVersion:     int32(result.TargetVersion),
		MigrationsApplied: int32(result.MigrationsApplied),
	}, nil
}

func mapRestoreMode(m backupV1.RestoreMode) backup.RestoreMode {
	if m == backupV1.RestoreMode_RESTORE_MODE_OVERWRITE {
		return backup.RestoreModeOverwrite
	}
	return backup.RestoreModeSkip
}

// --- Import helpers ---

func (s *BackupService) importTaskTypes(ctx context.Context, client *ent.Client, a *backup.Archive, mode backup.RestoreMode, result *backup.RestoreResult) {
	types, err := backup.GetEntities[ent.TaskType](a, "taskTypes")
	if err != nil {
		result.AddWarning(fmt.Sprintf("taskTypes: unmarshal error: %v", err))
		return
	}
	if len(types) == 0 {
		return
	}

	er := backup.EntityResult{EntityType: "taskTypes", Total: int64(len(types))}

	for _, e := range types {
		existing, getErr := client.TaskType.Query().Where(tasktype.TaskTypeEQ(e.TaskType)).First(ctx)
		if getErr != nil && !ent.IsNotFound(getErr) {
			result.AddWarning(fmt.Sprintf("taskTypes: lookup %s: %v", e.TaskType, getErr))
			er.Failed++
			continue
		}

		if existing != nil {
			if mode == backup.RestoreModeSkip {
				er.Skipped++
				continue
			}
			_, err := client.TaskType.UpdateOneID(existing.ID).
				SetModuleID(e.ModuleID).
				SetNillableDisplayName(e.DisplayName).
				SetNillableDescription(e.Description).
				SetNillablePayloadSchema(e.PayloadSchema).
				SetNillableDefaultCron(e.DefaultCron).
				SetDefaultMaxRetry(e.DefaultMaxRetry).
				Save(ctx)
			if err != nil {
				result.AddWarning(fmt.Sprintf("taskTypes: update %s: %v", e.TaskType, err))
				er.Failed++
				continue
			}
			er.Updated++
		} else {
			_, err := client.TaskType.Create().
				SetTaskType(e.TaskType).
				SetModuleID(e.ModuleID).
				SetNillableDisplayName(e.DisplayName).
				SetNillableDescription(e.Description).
				SetNillablePayloadSchema(e.PayloadSchema).
				SetNillableDefaultCron(e.DefaultCron).
				SetDefaultMaxRetry(e.DefaultMaxRetry).
				SetRegisteredAt(e.RegisteredAt).
				Save(ctx)
			if err != nil {
				result.AddWarning(fmt.Sprintf("taskTypes: create %s: %v", e.TaskType, err))
				er.Failed++
				continue
			}
			er.Created++
		}
	}

	result.AddResult(er)
}

func (s *BackupService) importTasks(ctx context.Context, client *ent.Client, a *backup.Archive, tenantID uint32, full bool, mode backup.RestoreMode, result *backup.RestoreResult) {
	tasks, err := backup.GetEntities[ent.Task](a, "tasks")
	if err != nil {
		result.AddWarning(fmt.Sprintf("tasks: unmarshal error: %v", err))
		return
	}
	if len(tasks) == 0 {
		return
	}

	er := backup.EntityResult{EntityType: "tasks", Total: int64(len(tasks))}

	for _, e := range tasks {
		tid := tenantID
		if full && e.TenantID != nil {
			tid = *e.TenantID
		}

		existing, getErr := client.Task.Get(ctx, e.ID)
		if getErr != nil && !ent.IsNotFound(getErr) {
			result.AddWarning(fmt.Sprintf("tasks: lookup %d: %v", e.ID, getErr))
			er.Failed++
			continue
		}

		if existing != nil {
			if mode == backup.RestoreModeSkip {
				er.Skipped++
				continue
			}
			_, err := client.Task.UpdateOneID(e.ID).
				SetNillableType(e.Type).
				SetNillableTypeName(e.TypeName).
				SetNillableModuleID(e.ModuleID).
				SetNillableTaskPayload(e.TaskPayload).
				SetNillableCronSpec(e.CronSpec).
				SetNillableEnable(e.Enable).
				SetNillableRemark(e.Remark).
				Save(ctx)
			if err != nil {
				result.AddWarning(fmt.Sprintf("tasks: update %d: %v", e.ID, err))
				er.Failed++
				continue
			}
			er.Updated++
		} else {
			builder := client.Task.Create().
				SetID(e.ID).
				SetNillableTenantID(&tid).
				SetNillableType(e.Type).
				SetNillableTypeName(e.TypeName).
				SetNillableModuleID(e.ModuleID).
				SetNillableTaskPayload(e.TaskPayload).
				SetNillableCronSpec(e.CronSpec).
				SetNillableEnable(e.Enable).
				SetNillableRemark(e.Remark).
				SetNillableCreatedBy(e.CreatedBy).
				SetNillableCreatedAt(e.CreatedAt)
			if e.TaskOptions != nil {
				builder.SetTaskOptions(e.TaskOptions)
			}
			_, err := builder.Save(ctx)
			if err != nil {
				result.AddWarning(fmt.Sprintf("tasks: create %d: %v", e.ID, err))
				er.Failed++
				continue
			}
			er.Created++
		}
	}

	result.AddResult(er)
}

func (s *BackupService) importTaskExecutions(ctx context.Context, client *ent.Client, a *backup.Archive, tenantID uint32, full bool, mode backup.RestoreMode, result *backup.RestoreResult) {
	executions, err := backup.GetEntities[ent.TaskExecution](a, "taskExecutions")
	if err != nil {
		result.AddWarning(fmt.Sprintf("taskExecutions: unmarshal error: %v", err))
		return
	}
	if len(executions) == 0 {
		return
	}

	er := backup.EntityResult{EntityType: "taskExecutions", Total: int64(len(executions))}

	for _, e := range executions {
		existing, getErr := client.TaskExecution.Query().Where(taskexecution.ExecutionIDEQ(e.ExecutionID)).First(ctx)
		if getErr != nil && !ent.IsNotFound(getErr) {
			result.AddWarning(fmt.Sprintf("taskExecutions: lookup %s: %v", e.ExecutionID, getErr))
			er.Failed++
			continue
		}

		if existing != nil {
			er.Skipped++
			continue
		}

		tid := tenantID
		if full {
			tid = e.TenantID
		}

		_, err := client.TaskExecution.Create().
			SetExecutionID(e.ExecutionID).
			SetTaskType(e.TaskType).
			SetModuleID(e.ModuleID).
			SetStatus(e.Status).
			SetNillableMessage(e.Message).
			SetAttempt(e.Attempt).
			SetDurationMs(e.DurationMs).
			SetStartedAt(e.StartedAt).
			SetNillableFinishedAt(e.FinishedAt).
			SetTenantID(tid).
			Save(ctx)
		if err != nil {
			result.AddWarning(fmt.Sprintf("taskExecutions: create %s: %v", e.ExecutionID, err))
			er.Failed++
			continue
		}
		er.Created++
	}

	result.AddResult(er)
}
