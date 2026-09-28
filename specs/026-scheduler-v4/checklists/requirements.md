# Specification Quality Checklist: Scheduler Module for v4

**Purpose**: Check that the specification is complete and of good quality
before moving to planning.

**Created**: 2026-09-28 · **Feature**: [spec.md](../spec.md)

## Content Quality

- [x] No implementation details (no languages, frameworks, queue or
      storage engines)
- [x] Focused on user value and business needs
- [x] Written for non-technical stakeholders (module and mesh terms are only
      used where they are the product's own concepts)
- [x] All mandatory sections completed

## Requirement Completeness

- [x] No [NEEDS CLARIFICATION] markers remain. Both were resolved with the
      user on 2026-09-28:
      1. Tenancy: tasks are per tenant, and a type can be platform-scoped
         (FR-025–FR-027).
      2. Built-in module jobs stay internal (FR-028).
- [x] Requirements are testable and unambiguous
- [x] Success criteria are measurable
- [x] Success criteria are technology-agnostic
- [x] All acceptance scenarios are defined
- [x] Edge cases are identified: module offline, type unavailable, schema
      change, duplicates, overlap, missed runs, DST, long runs, multiple
      instances, deletion, oversize
- [x] Scope is clearly bounded (backup task types, workflows and data
      migration are out of scope)
- [x] Dependencies and assumptions identified

## Feature Readiness

- [x] All functional requirements have clear acceptance criteria
- [x] User stories cover the primary flows: register types and schedule, run
      history, one-shot kinds, control, ported types, overview
- [x] The feature meets the measurable outcomes defined in Success Criteria
- [x] No implementation details leak into the specification

## v3 Parity

- [x] Every v3 operation has a v4 equivalent:
  - RegisterTaskTypes, UnregisterTaskTypes, ListRegisteredTaskTypes
  - ListTasks, GetTask, CreateTask, UpdateTask, DeleteTask
  - ListTaskTypeNames
  - RestartAllTasks, StartAllTasks, StopAllTasks
  - ControlTask
  - ListTaskExecutions
- [x] The v3 kinds (periodic, delay, wait-result), retries and permanent
      failure, last-run fields, history drawer and failed filter, and
      permission codes and roles are kept
- [x] The v3 backup export/import of scheduler data is kept (FR-024)
- [x] v3 defects are explicitly not carried over:
  - no tenant isolation
  - permissions not enforced
  - module id trusted from the request
  - payload not validated
  - one task per type
  - one-shot tasks re-firing
  - result discarded
  - empty metrics
  - blocking wait-result

## Notes

- The clarifications were resolved on 2026-09-28: tenancy option A and
  built-in jobs option A. The spec is ready for `/speckit.plan`.
- Planning decisions intentionally left open:
  - execution engine and storage
  - exact backoff curve
  - where the shared v4 execution contract is published
  - the lcm → notification call path
