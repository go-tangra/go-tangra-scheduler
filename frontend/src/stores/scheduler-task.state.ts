import { defineStore } from 'pinia';

import {
  taskService,
  taskTypeService,
  type Task,
  type ListTasksResponse,
  type ListTaskExecutionsResponse,
  type ListRegisteredTaskTypesResponse,
  type RestartAllTasksResponse,
  type ControlTaskRequest_ControlType,
} from '../api/client';

export const useSchedulerTaskStore = defineStore('scheduler-task', () => {
  async function listTasks(
    paging?: { page?: number; pageSize?: number },
  ): Promise<ListTasksResponse> {
    return await taskService.ListTasks({
      page: paging?.page,
      pageSize: paging?.pageSize,
    });
  }

  async function getTask(id: number): Promise<Task> {
    return await taskService.GetTask({ id }) as any;
  }

  async function createTask(data: Partial<Task>): Promise<Task> {
    return await taskService.CreateTask({ data: data as any }) as any;
  }

  async function updateTask(
    id: number,
    data: Partial<Task>,
    updateMask?: string,
  ): Promise<Task> {
    return await taskService.UpdateTask({ id, data: data as any, updateMask }) as any;
  }

  async function deleteTask(id: number): Promise<void> {
    await taskService.DeleteTask({ id });
  }

  async function listTypeNames() {
    return await taskService.ListTaskTypeNames({});
  }

  async function listRegisteredTaskTypes(): Promise<ListRegisteredTaskTypesResponse> {
    return await taskTypeService.ListRegisteredTaskTypes({}) as any;
  }

  async function controlTask(
    controlType: ControlTaskRequest_ControlType,
    typeName: string,
  ): Promise<void> {
    await taskService.ControlTask({ controlType, typeName });
  }

  async function startAllTasks(): Promise<void> {
    await taskService.StartAllTasks({});
  }

  async function stopAllTasks(): Promise<void> {
    await taskService.StopAllTasks({});
  }

  async function listExecutions(
    taskType: string,
    page?: number,
    pageSize?: number,
  ): Promise<ListTaskExecutionsResponse> {
    return await taskService.ListTaskExecutions({ taskType, page, pageSize }) as any;
  }

  async function restartAllTasks(): Promise<RestartAllTasksResponse> {
    return await taskService.RestartAllTasks({}) as any;
  }

  function $reset() {}

  return {
    $reset,
    listTasks,
    getTask,
    createTask,
    updateTask,
    deleteTask,
    listTypeNames,
    listRegisteredTaskTypes,
    controlTask,
    startAllTasks,
    stopAllTasks,
    restartAllTasks,
    listExecutions,
  };
});
