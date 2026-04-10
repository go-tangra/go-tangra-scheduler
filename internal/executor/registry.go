package executor

import (
	"strings"
	"sync"
	"time"
)

// TaskTypeEntry holds metadata about a registered task type.
type TaskTypeEntry struct {
	ModuleID      string
	TaskType      string
	DisplayName   string
	Description   string
	PayloadSchema string
	DefaultCron   string
	DefaultRetry  int32
	RegisteredAt  time.Time
}

// TaskTypeRegistry is a thread-safe in-memory map of task_type -> TaskTypeEntry.
type TaskTypeRegistry struct {
	mu      sync.RWMutex
	entries map[string]TaskTypeEntry
}

func NewTaskTypeRegistry() *TaskTypeRegistry {
	return &TaskTypeRegistry{
		entries: make(map[string]TaskTypeEntry),
	}
}

// Register adds or updates a task type entry.
func (r *TaskTypeRegistry) Register(entry TaskTypeEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[entry.TaskType] = entry
}

// Unregister removes all entries for a module.
func (r *TaskTypeRegistry) UnregisterModule(moduleID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for k, v := range r.entries {
		if v.ModuleID == moduleID {
			delete(r.entries, k)
			count++
		}
	}
	return count
}

// Lookup returns the entry for a task type.
func (r *TaskTypeRegistry) Lookup(taskType string) (TaskTypeEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.entries[taskType]
	return entry, ok
}

// ResolveModuleID returns the module_id for a task type.
// Falls back to extracting the prefix before ":" if not found in registry.
func (r *TaskTypeRegistry) ResolveModuleID(taskType string) (string, bool) {
	entry, ok := r.Lookup(taskType)
	if ok {
		return entry.ModuleID, true
	}
	// Fallback: extract module_id from "{module_id}:{action}" convention
	moduleID, _, found := strings.Cut(taskType, ":")
	if found && moduleID != "" {
		return moduleID, true
	}
	return "", false
}

// ListAll returns all registered entries.
func (r *TaskTypeRegistry) ListAll() []TaskTypeEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]TaskTypeEntry, 0, len(r.entries))
	for _, e := range r.entries {
		result = append(result, e)
	}
	return result
}

// GetRegisteredTaskTypes returns all task type names.
func (r *TaskTypeRegistry) GetRegisteredTaskTypes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]string, 0, len(r.entries))
	for k := range r.entries {
		result = append(result, k)
	}
	return result
}

// TaskTypeExists checks if a task type is registered.
func (r *TaskTypeRegistry) TaskTypeExists(taskType string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.entries[taskType]
	return ok
}
