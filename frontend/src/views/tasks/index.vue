<script lang="ts" setup>
import type { VxeGridProps } from 'shell/adapter/vxe-table';

import { h, computed } from 'vue';

import { Page, useVbenDrawer, type VbenFormProps } from 'shell/vben/common-ui';
import { LucidePencil, LucideTrash, LucideCirclePlay, LucidePlus } from 'shell/vben/icons';

import { notification, Space, Button, Tag, Dropdown, Menu, MenuItem, Tooltip } from 'ant-design-vue';

import { useVbenVxeGrid } from 'shell/adapter/vxe-table';
import { $t } from 'shell/locales';
import { useSchedulerTaskStore } from '../../stores/scheduler-task.state';
import type { Task } from '../../api/client';

import TaskDrawer from './task-drawer.vue';

const taskStore = useSchedulerTaskStore();

const taskTypeOptions = computed(() => [
  { value: 'PERIODIC', label: $t('scheduler.page.task.typePeriodic') },
  { value: 'DELAY', label: $t('scheduler.page.task.typeDelay') },
  { value: 'WAIT_RESULT', label: $t('scheduler.page.task.typeWaitResult') },
]);

function taskTypeLabel(type: string | undefined) {
  const opt = taskTypeOptions.value.find((o) => o.value === type);
  return opt?.label ?? type ?? '';
}

function taskTypeColor(type: string | undefined) {
  switch (type) {
    case 'PERIODIC': return '#1890FF';
    case 'DELAY': return '#722ED1';
    case 'WAIT_RESULT': return '#FA8C16';
    default: return '#999';
  }
}

const formOptions: VbenFormProps = {
  collapsed: false,
  showCollapseButton: false,
  submitOnEnter: true,
  schema: [
    {
      component: 'Select',
      fieldName: 'type',
      label: $t('scheduler.page.task.taskType'),
      componentProps: {
        options: taskTypeOptions,
        placeholder: $t('ui.placeholder.select'),
        allowClear: true,
      },
    },
    {
      component: 'Input',
      fieldName: 'typeName',
      label: $t('scheduler.page.task.typeName'),
      componentProps: {
        placeholder: $t('ui.placeholder.input'),
        allowClear: true,
      },
    },
  ],
};

const gridOptions: VxeGridProps<Task> = {
  height: 'auto',
  stripe: false,
  toolbarConfig: {
    custom: true,
    export: true,
    import: false,
    refresh: true,
    zoom: true,
  },
  exportConfig: {},
  rowConfig: {
    isHover: true,
  },
  pagerConfig: {
    enabled: true,
    pageSize: 20,
    pageSizes: [10, 20, 50, 100],
  },

  proxyConfig: {
    ajax: {
      query: async ({ page }) => {
        const resp = await taskStore.listTasks({
          page: page.currentPage,
          pageSize: page.pageSize,
        });
        return {
          items: resp.items ?? [],
          total: Number(resp.total) || 0,
        };
      },
    },
  },

  columns: [
    { title: '#', type: 'seq', width: 40 },
    {
      title: 'Task Type',
      field: 'typeName',
      minWidth: 140,
      slots: { default: 'taskTypeName' },
    },
    {
      title: 'Schedule',
      field: 'type',
      width: 100,
      slots: { default: 'taskType' },
    },
    {
      title: 'Cron',
      field: 'cronSpec',
      width: 120,
    },
    {
      title: 'Status',
      field: 'enable',
      width: 80,
      slots: { default: 'enableStatus' },
    },
    {
      title: 'Last Run',
      field: 'lastRunStatus',
      width: 90,
      slots: { default: 'lastRunStatus' },
    },
    {
      title: 'Last Run At',
      field: 'lastRunAt',
      width: 150,
    },
    {
      title: 'Runs',
      field: 'runCount',
      width: 50,
    },
    {
      title: $t('ui.table.action'),
      field: 'action',
      fixed: 'right',
      slots: { default: 'action' },
      width: 160,
    },
  ],
};

const [Grid, gridApi] = useVbenVxeGrid({ gridOptions, formOptions });

const [TaskDrawerComponent, taskDrawerApi] = useVbenDrawer({
  connectedComponent: TaskDrawer,
  onOpenChange(isOpen: boolean) {
    if (!isOpen) {
      gridApi.query();
    }
  },
});

function handleCreate() {
  taskDrawerApi.setData({ mode: 'create' });
  taskDrawerApi.open();
}

function handleEdit(row: Task) {
  taskDrawerApi.setData({ row, mode: 'edit' });
  taskDrawerApi.open();
}

function handleView(row: Task) {
  taskDrawerApi.setData({ row, mode: 'view' });
  taskDrawerApi.open();
}

async function handleDelete(row: Task) {
  if (!row.id) return;
  try {
    await taskStore.deleteTask(row.id);
    notification.success({ message: $t('scheduler.page.task.deleteSuccess') });
    await gridApi.query();
  } catch {
    notification.error({ message: $t('ui.notification.delete_failed') });
  }
}

async function handleControl(row: Task, action: 'START' | 'STOP' | 'RESTART') {
  if (!row.typeName) return;
  try {
    await taskStore.controlTask(action, row.typeName);
    const msgKey = action === 'START'
      ? 'scheduler.page.task.startSuccess'
      : action === 'STOP'
        ? 'scheduler.page.task.stopSuccess'
        : 'scheduler.page.task.restartSuccess';
    notification.success({ message: $t(msgKey) });
    await gridApi.query();
  } catch {
    notification.error({ message: $t('ui.notification.operation_failed') });
  }
}

async function handleStartAll() {
  try {
    await taskStore.startAllTasks();
    notification.success({ message: $t('scheduler.page.task.startAllSuccess') });
    await gridApi.query();
  } catch {
    notification.error({ message: $t('ui.notification.operation_failed') });
  }
}

async function handleStopAll() {
  try {
    await taskStore.stopAllTasks();
    notification.success({ message: $t('scheduler.page.task.stopAllSuccess') });
    await gridApi.query();
  } catch {
    notification.error({ message: $t('ui.notification.operation_failed') });
  }
}

async function handleRestartAll() {
  try {
    const resp = await taskStore.restartAllTasks();
    notification.success({
      message: `All tasks restarted (${resp.count} tasks)`,
    });
    await gridApi.query();
  } catch {
    notification.error({ message: $t('ui.notification.operation_failed') });
  }
}
</script>

<template>
  <Page auto-content-height>
    <div class="mb-3 flex items-center justify-between">
      <h3 class="m-0 text-lg font-semibold">{{ $t('scheduler.page.task.title') }}</h3>
      <Space>
        <Button type="primary" :icon="h(LucidePlus)" @click="handleCreate">
          {{ $t('scheduler.page.task.create') }}
        </Button>
        <Dropdown>
          <Button>
            {{ $t('scheduler.page.task.restartAll') }}
          </Button>
          <template #overlay>
            <Menu>
              <MenuItem key="start-all">
                <a-popconfirm
                  :title="$t('scheduler.page.task.confirmStartAll')"
                  @confirm="handleStartAll"
                >
                  <span>{{ $t('scheduler.page.task.startAll') }}</span>
                </a-popconfirm>
              </MenuItem>
              <MenuItem key="stop-all">
                <a-popconfirm
                  :title="$t('scheduler.page.task.confirmStopAll')"
                  @confirm="handleStopAll"
                >
                  <span>{{ $t('scheduler.page.task.stopAll') }}</span>
                </a-popconfirm>
              </MenuItem>
              <MenuItem key="restart-all">
                <a-popconfirm
                  :title="$t('scheduler.page.task.confirmRestartAll')"
                  @confirm="handleRestartAll"
                >
                  <span>{{ $t('scheduler.page.task.restartAll') }}</span>
                </a-popconfirm>
              </MenuItem>
            </Menu>
          </template>
        </Dropdown>
      </Space>
    </div>

    <Grid>

      <template #taskTypeName="{ row }">
        <div>
          <span>{{ row.typeName }}</span>
          <Tag v-if="row.moduleId" color="#108ee9" class="ml-1" style="font-size: 10px">{{ row.moduleId }}</Tag>
        </div>
      </template>

      <template #taskType="{ row }">
        <Tag :color="taskTypeColor(row.type)">
          {{ taskTypeLabel(row.type) }}
        </Tag>
      </template>

      <template #lastRunStatus="{ row }">
        <Tooltip v-if="row.lastRunMessage" :title="row.lastRunMessage">
          <Tag
            :color="row.lastRunStatus === 'success' ? '#52C41A' : row.lastRunStatus === 'failed' ? '#FF4D4F' : row.lastRunStatus === 'running' ? '#1890FF' : '#999'"
          >
            {{ row.lastRunStatus || 'Never' }}
          </Tag>
        </Tooltip>
        <Tag v-else color="#999">Never</Tag>
      </template>

      <template #enableStatus="{ row }">
        <Tag :color="row.enable ? '#52C41A' : '#FF4D4F'">
          {{ row.enable ? $t('scheduler.page.task.enabled') : $t('scheduler.page.task.disabled') }}
        </Tag>
      </template>

      <template #action="{ row }">
        <Space :size="4">
          <Button
            type="link"
            size="small"
            :icon="h(LucideCirclePlay)"
            :title="$t('scheduler.page.task.start')"
            @click.stop="handleControl(row, 'START')"
          />
          <Button
            type="link"
            size="small"
            :icon="h(LucidePencil)"
            :title="$t('scheduler.page.task.edit')"
            @click.stop="handleEdit(row)"
          />
          <a-popconfirm
            :cancel-text="$t('ui.button.cancel')"
            :ok-text="$t('ui.button.ok')"
            :title="$t('scheduler.page.task.confirmDelete')"
            @confirm="handleDelete(row)"
          >
            <Button
              danger
              type="link"
              size="small"
              :icon="h(LucideTrash)"
              :title="$t('scheduler.page.task.delete')"
            />
          </a-popconfirm>
        </Space>
      </template>
    </Grid>

    <TaskDrawerComponent />
  </Page>
</template>
