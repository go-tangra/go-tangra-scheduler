<script lang="ts" setup>
import { ref, computed } from 'vue';

import { useVbenDrawer } from 'shell/vben/common-ui';

import {
  Form,
  FormItem,
  Input,
  InputNumber,
  Button,
  notification,
  Select,
  Switch,
  Descriptions,
  DescriptionsItem,
  Tag,
  Collapse,
  CollapsePanel,
  Table,
  TableColumn,
  Divider,
} from 'ant-design-vue';

import { $t } from 'shell/locales';
import { useSchedulerTaskStore } from '../../stores/scheduler-task.state';
import type { Task, Task_Type, TaskOption, RegisteredTaskType, TaskExecution } from '../../api/client';

const taskStore = useSchedulerTaskStore();

const data = ref<{
  mode: 'create' | 'edit' | 'view';
  row?: Task;
}>();
const loading = ref(false);
const registeredTypes = ref<RegisteredTaskType[]>([]);
const executions = ref<TaskExecution[]>([]);
const executionsLoading = ref(false);

const formState = ref<{
  typeName: string;
  type: Task_Type;
  cronSpec: string;
  taskPayload: string;
  enable: boolean;
  remark: string;
  maxRetry: number | undefined;
  timeout: string;
  group: string;
  taskID: string;
}>({
  typeName: '',
  type: 'PERIODIC',
  cronSpec: '',
  taskPayload: '',
  enable: false,
  remark: '',
  maxRetry: undefined,
  timeout: '',
  group: '',
  taskID: '',
});

const taskTypeOptions = computed(() => [
  { value: 'PERIODIC', label: $t('scheduler.page.task.typePeriodic') },
  { value: 'DELAY', label: $t('scheduler.page.task.typeDelay') },
  { value: 'WAIT_RESULT', label: $t('scheduler.page.task.typeWaitResult') },
]);

const title = computed(() => {
  switch (data.value?.mode) {
    case 'create': return $t('scheduler.page.task.create');
    case 'edit': return $t('scheduler.page.task.edit');
    default: return $t('scheduler.page.task.view');
  }
});

const isViewMode = computed(() => data.value?.mode === 'view');
const isCreateMode = computed(() => data.value?.mode === 'create');
const isPeriodic = computed(() => formState.value.type === 'PERIODIC');

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

function populateForm(task: Task) {
  formState.value = {
    typeName: task.typeName ?? '',
    type: task.type ?? 'PERIODIC',
    cronSpec: task.cronSpec ?? '',
    taskPayload: task.taskPayload ?? '',
    enable: task.enable ?? false,
    remark: task.remark ?? '',
    maxRetry: task.taskOptions?.maxRetry,
    timeout: task.taskOptions?.timeout ?? '',
    group: task.taskOptions?.group ?? '',
    taskID: task.taskOptions?.taskID ?? '',
  };
}

function resetForm() {
  formState.value = {
    typeName: '',
    type: 'PERIODIC',
    cronSpec: '',
    taskPayload: '',
    enable: false,
    remark: '',
    maxRetry: undefined,
    timeout: '',
    group: '',
    taskID: '',
  };
}

function buildTaskData(): Task {
  const taskOptions: TaskOption = {};
  if (formState.value.maxRetry !== undefined && formState.value.maxRetry > 0) {
    taskOptions.maxRetry = formState.value.maxRetry;
  }
  if (formState.value.timeout) {
    taskOptions.timeout = formState.value.timeout;
  }
  if (formState.value.group) {
    taskOptions.group = formState.value.group;
  }
  if (formState.value.taskID) {
    taskOptions.taskID = formState.value.taskID;
  }

  return {
    typeName: formState.value.typeName,
    type: formState.value.type,
    cronSpec: formState.value.cronSpec || undefined,
    taskPayload: formState.value.taskPayload || undefined,
    enable: formState.value.enable,
    remark: formState.value.remark || undefined,
    taskOptions: Object.keys(taskOptions).length > 0 ? taskOptions : undefined,
  };
}

async function handleSubmit() {
  loading.value = true;
  try {
    const taskData = buildTaskData();

    if (isCreateMode.value) {
      await taskStore.createTask(taskData);
      notification.success({ message: $t('scheduler.page.task.createSuccess') });
    } else {
      const id = data.value?.row?.id;
      if (!id) return;
      await taskStore.updateTask(id, taskData);
      notification.success({ message: $t('scheduler.page.task.updateSuccess') });
    }
    drawerApi.close();
  } catch (e) {
    console.error('Failed to save task:', e);
    notification.error({ message: $t('ui.notification.create_failed') });
  } finally {
    loading.value = false;
  }
}

async function loadExecutions(taskType: string) {
  executionsLoading.value = true;
  try {
    const resp = await taskStore.listExecutions(taskType, 1, 20);
    executions.value = resp?.items ?? [];
  } catch {
    executions.value = [];
  } finally {
    executionsLoading.value = false;
  }
}

async function loadRegisteredTypes() {
  try {
    const resp = await taskStore.listRegisteredTaskTypes();
    registeredTypes.value = resp?.taskTypes ?? [];
  } catch {
    registeredTypes.value = [];
  }
}

function onTaskTypeSelected(taskType: string) {
  if (!isCreateMode.value) return;
  const reg = registeredTypes.value.find((t) => t.taskType === taskType);
  if (!reg) return;
  // Auto-populate defaults from the registered task type
  if (reg.defaultCron && !formState.value.cronSpec) {
    formState.value.cronSpec = reg.defaultCron;
  }
  if (reg.defaultMaxRetry && !formState.value.maxRetry) {
    formState.value.maxRetry = reg.defaultMaxRetry;
  }
}

const [Drawer, drawerApi] = useVbenDrawer({
  onCancel() {
    drawerApi.close();
  },

  async onOpenChange(isOpen) {
    if (isOpen) {
      data.value = drawerApi.getData() as {
        mode: 'create' | 'edit' | 'view';
        row?: Task;
      };

      await loadRegisteredTypes();

      if (data.value?.mode === 'create') {
        resetForm();
        executions.value = [];
      } else if (data.value?.row) {
        populateForm(data.value.row);
        if (data.value.row.typeName) {
          loadExecutions(data.value.row.typeName);
        }
      }
    }
  },
});

const task = computed(() => data.value?.row);
</script>

<template>
  <Drawer :title="title" :footer="false">
    <!-- View Mode -->
    <template v-if="task && isViewMode">
      <Descriptions :column="1" bordered size="small">
        <DescriptionsItem :label="$t('scheduler.page.task.typeName')">
          {{ task.typeName || '-' }}
        </DescriptionsItem>
        <DescriptionsItem label="Module">
          <Tag v-if="task.moduleId" color="#108ee9">{{ task.moduleId }}</Tag>
          <span v-else>-</span>
        </DescriptionsItem>
        <DescriptionsItem :label="$t('scheduler.page.task.taskType')">
          <Tag :color="taskTypeColor(task.type)">
            {{ taskTypeLabel(task.type) }}
          </Tag>
        </DescriptionsItem>
        <DescriptionsItem v-if="task.cronSpec" :label="$t('scheduler.page.task.cronSpec')">
          <code>{{ task.cronSpec }}</code>
        </DescriptionsItem>
        <DescriptionsItem :label="$t('scheduler.page.task.enable')">
          <Tag :color="task.enable ? '#52C41A' : '#FF4D4F'">
            {{ task.enable ? $t('scheduler.page.task.enabled') : $t('scheduler.page.task.disabled') }}
          </Tag>
        </DescriptionsItem>
        <DescriptionsItem v-if="task.remark" :label="$t('scheduler.page.task.remark')">
          {{ task.remark }}
        </DescriptionsItem>
        <DescriptionsItem v-if="task.taskPayload" :label="$t('scheduler.page.task.taskPayload')">
          <pre class="m-0 whitespace-pre-wrap text-xs">{{ task.taskPayload }}</pre>
        </DescriptionsItem>
        <DescriptionsItem :label="$t('scheduler.page.task.createdAt')">
          {{ task.createdAt || '-' }}
        </DescriptionsItem>
        <DescriptionsItem v-if="task.updatedAt" :label="$t('scheduler.page.task.updatedAt')">
          {{ task.updatedAt }}
        </DescriptionsItem>
      </Descriptions>

      <!-- Task Options -->
      <template v-if="task.taskOptions">
        <Collapse class="mt-4">
          <CollapsePanel :header="$t('scheduler.page.task.options')" key="options">
            <Descriptions :column="1" bordered size="small">
              <DescriptionsItem
                v-if="task.taskOptions.maxRetry"
                :label="$t('scheduler.page.task.maxRetry')"
              >
                {{ task.taskOptions.maxRetry }}
              </DescriptionsItem>
              <DescriptionsItem
                v-if="task.taskOptions.timeout"
                :label="$t('scheduler.page.task.timeout')"
              >
                {{ task.taskOptions.timeout }}
              </DescriptionsItem>
              <DescriptionsItem
                v-if="task.taskOptions.group"
                :label="$t('scheduler.page.task.group')"
              >
                {{ task.taskOptions.group }}
              </DescriptionsItem>
              <DescriptionsItem
                v-if="task.taskOptions.taskID"
                :label="$t('scheduler.page.task.taskID')"
              >
                {{ task.taskOptions.taskID }}
              </DescriptionsItem>
            </Descriptions>
          </CollapsePanel>
        </Collapse>
      </template>

    </template>

    <!-- Create / Edit Mode -->
    <template v-else-if="!isViewMode">
      <Form layout="vertical" :model="formState" @finish="handleSubmit">
        <FormItem
          :label="$t('scheduler.page.task.typeName')"
          name="typeName"
          :rules="[{ required: true, message: $t('ui.formRules.required') }]"
        >
          <Select
            v-if="registeredTypes.length > 0 && isCreateMode"
            v-model:value="formState.typeName"
            :options="registeredTypes.map((t) => ({
              value: t.taskType,
              label: t.displayName ? `${t.displayName} (${t.moduleId})` : t.taskType,
            }))"
            :placeholder="$t('ui.placeholder.select')"
            show-search
            allow-clear
            @change="onTaskTypeSelected"
          />
          <Input
            v-else
            v-model:value="formState.typeName"
            :placeholder="$t('ui.placeholder.input')"
            :disabled="!isCreateMode"
          />
        </FormItem>

        <FormItem
          :label="$t('scheduler.page.task.taskType')"
          name="type"
          :rules="[{ required: true, message: $t('ui.formRules.required') }]"
        >
          <Select
            v-model:value="formState.type"
            :options="taskTypeOptions"
          />
        </FormItem>

        <FormItem
          v-if="isPeriodic"
          :label="$t('scheduler.page.task.cronSpec')"
          name="cronSpec"
          :rules="[{ required: isPeriodic, message: $t('ui.formRules.required') }]"
        >
          <Input
            v-model:value="formState.cronSpec"
            :placeholder="$t('scheduler.page.task.cronSpecPlaceholder')"
          />
        </FormItem>

        <FormItem :label="$t('scheduler.page.task.enable')" name="enable">
          <Switch v-model:checked="formState.enable" />
        </FormItem>

        <FormItem :label="$t('scheduler.page.task.remark')" name="remark">
          <Input v-model:value="formState.remark" />
        </FormItem>

        <FormItem label="Task Payload (JSON)">
          <Input v-model:value="formState.taskPayload" placeholder='{"key": "value"}' />
        </FormItem>

        <!-- Task Options -->
        <Collapse>
          <CollapsePanel :header="$t('scheduler.page.task.options')" key="options">
            <FormItem :label="$t('scheduler.page.task.maxRetry')" name="maxRetry">
              <InputNumber
                v-model:value="formState.maxRetry"
                :min="0"
                :max="100"
                style="width: 100%"
              />
            </FormItem>

            <FormItem :label="$t('scheduler.page.task.timeout')" name="timeout">
              <Input
                v-model:value="formState.timeout"
                placeholder="e.g. 30s, 5m, 1h"
              />
            </FormItem>

            <FormItem :label="$t('scheduler.page.task.group')" name="group">
              <Input v-model:value="formState.group" />
            </FormItem>

            <FormItem :label="$t('scheduler.page.task.taskID')" name="taskID">
              <Input v-model:value="formState.taskID" />
            </FormItem>
          </CollapsePanel>
        </Collapse>

        <FormItem class="mt-4">
          <Button type="primary" html-type="submit" :loading="loading" block>
            {{ isCreateMode ? $t('scheduler.page.task.create') : $t('scheduler.page.task.edit') }}
          </Button>
        </FormItem>
      </Form>
    </template>

    <!-- Execution History (shown in view and edit modes) -->
    <template v-if="!isCreateMode && executions.length > 0">
      <Divider />
      <h4 class="m-0 mb-3 text-base font-medium">Execution History</h4>
      <Table
        :data-source="executions"
        :loading="executionsLoading"
        :pagination="false"
        size="small"
        row-key="executionId"
        :scroll="{ y: 250 }"
      >
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'status'">
            <Tag :color="record.status === 'success' ? '#52C41A' : '#FF4D4F'">
              {{ record.status }}
            </Tag>
          </template>
          <template v-else-if="column.key === 'duration'">
            {{ record.durationMs ? `${record.durationMs}ms` : '-' }}
          </template>
        </template>
        <TableColumn key="startedAt" title="Time" data-index="startedAt" :width="160" />
        <TableColumn key="status" title="Status" data-index="status" :width="80" />
        <TableColumn key="duration" title="Duration" data-index="durationMs" :width="80" />
        <TableColumn key="message" title="Message" data-index="message" :ellipsis="true" />
      </Table>
    </template>
  </Drawer>
</template>
