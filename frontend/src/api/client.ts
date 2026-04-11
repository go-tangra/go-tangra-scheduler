/**
 * Scheduler Module API Client
 *
 * Uses buf-generated TypeScript clients from protoc-gen-typescript-http.
 * All types and service methods are auto-generated from protos.
 */

import { useAccessStore } from 'shell/vben/stores';

import {
  createSchedulerTaskServiceClient,
  createTaskTypeRegistrationServiceClient,
} from '../generated/api/scheduler/service/v1';

const MODULE_BASE_URL = '/admin/v1/modules/scheduler';

type RequestType = {
  path: string;
  method: string;
  body: string | null;
};

async function handler(req: RequestType): Promise<unknown> {
  const accessStore = useAccessStore();
  const token = accessStore.accessToken;

  const response = await fetch(`${MODULE_BASE_URL}/${req.path}`, {
    method: req.method,
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body: req.body,
  });

  if (!response.ok) {
    let message = `HTTP error! status: ${response.status}`;
    try {
      const text = await response.text();
      try {
        const errorBody = JSON.parse(text);
        if (errorBody?.message) {
          message = errorBody.message;
        }
      } catch { /* not JSON */ }
    } catch { /* failed to read */ }
    throw new Error(message);
  }

  const text = await response.text();
  if (!text) return {};
  return JSON.parse(text);
}

// Generated typed service clients
export const taskService = createSchedulerTaskServiceClient(handler);
export const taskTypeService = createTaskTypeRegistrationServiceClient(handler);

// Re-export all generated types for convenience
export type {
  Task,
  Task_Type,
  TaskOption,
  TaskExecution,
  ListTasksRequest,
  ListTasksResponse,
  GetTaskRequest,
  CreateTaskRequest,
  UpdateTaskRequest,
  DeleteTaskRequest,
  ControlTaskRequest,
  ControlTaskRequest_ControlType,
  RestartAllTasksResponse,
  ListTaskTypeNamesResponse,
  ListTaskExecutionsRequest,
  ListTaskExecutionsResponse,
  RegisteredTaskType,
  ListRegisteredTaskTypesResponse,
  RegisterTaskTypesRequest,
  RegisterTaskTypesResponse,
  TaskTypeDescriptor,
} from '../generated/api/scheduler/service/v1';
