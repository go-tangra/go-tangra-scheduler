<script lang="ts" setup>
import { computed, ref, watch } from 'vue';

import { useVbenDrawer } from 'shell/vben/common-ui';
import { $t } from 'shell/locales';

import { formatDateTime } from '../../datetime';

import {
  Alert,
  Button,
  Empty,
  Pagination,
  Radio,
  RadioGroup,
  Space,
  Spin,
  Statistic,
  Tag,
  Tooltip,
  Typography,
} from 'ant-design-vue';

import { useSchedulerTaskStore } from '../../stores/scheduler-task.state';
import type { Task, TaskExecution } from '../../api/client';

const taskStore = useSchedulerTaskStore();

interface DrawerData {
  row?: Task;
}

const [Drawer, drawerApi] = useVbenDrawer({
  onConfirm() {
    drawerApi.close();
  },
  onOpenChange(open: boolean) {
    if (open) {
      // Reset state every time the drawer opens. setData() runs
      // before onOpenChange fires, so the data is already in place.
      const data = drawerApi.getData<DrawerData>();
      task.value = data?.row ?? null;
      executions.value = [];
      total.value = 0;
      page.value = 1;
      showFailedOnly.value = false;
      if (task.value?.typeName) {
        void load();
      }
    }
  },
});

const task = ref<Task | null>(null);
const executions = ref<TaskExecution[]>([]);
const total = ref(0);
const page = ref(1);
const pageSize = ref(20);
const loading = ref(false);

// "Failed only" is a client-side filter applied AFTER fetch. The list
// RPC has no status filter and adding one would mean a proto change —
// for the task-history use case, paginated client-side filtering is
// fine since each page is small and operators usually want to see the
// most recent N runs regardless of outcome.
const showFailedOnly = ref(false);

async function load() {
  if (!task.value?.typeName) return;
  loading.value = true;
  try {
    const resp = await taskStore.listExecutions(
      task.value.typeName,
      page.value,
      pageSize.value,
    );
    executions.value = resp.items ?? [];
    total.value = Number(resp.total) || 0;
  } finally {
    loading.value = false;
  }
}

const filtered = computed(() =>
  showFailedOnly.value
    ? executions.value.filter((e) => e.status === 'failed')
    : executions.value,
);

const failedCount = computed(
  () => executions.value.filter((e) => e.status === 'failed').length,
);

const successCount = computed(
  () => executions.value.filter((e) => e.status === 'success').length,
);

function statusColor(status: string | undefined) {
  switch (status) {
    case 'success':
      return '#52C41A';
    case 'failed':
      return '#FF4D4F';
    case 'running':
      return '#1890FF';
    default:
      return '#999';
  }
}

function formatTimestamp(ts: string | undefined): string {
  if (!ts) return '—';
  return formatDateTime(ts);
}

function formatDuration(ms: number | string | undefined): string {
  const n = Number(ms ?? 0);
  if (n <= 0) return '—';
  if (n < 1000) return `${n}ms`;
  if (n < 60_000) return `${(n / 1000).toFixed(1)}s`;
  const s = Math.floor(n / 1000);
  return `${Math.floor(s / 60)}m ${s % 60}s`;
}

watch([page, pageSize], () => {
  void load();
});

async function refresh() {
  await load();
}
</script>

<template>
  <Drawer :title="$t('scheduler.page.history.title')" :width="900">
    <div v-if="task" class="flex flex-col gap-3">
      <!-- Task header: name, module, current state -->
      <Alert
        :type="task.lastRunStatus === 'failed' ? 'error' : task.lastRunStatus === 'success' ? 'success' : 'info'"
        show-icon
      >
        <template #message>
          <Space>
            <Typography.Text strong>{{ task.typeName }}</Typography.Text>
            <Tag v-if="task.moduleId" color="#108ee9" style="font-size: 10px">{{ task.moduleId }}</Tag>
            <Tag :color="task.enable ? '#52C41A' : '#FF4D4F'">
              {{ task.enable ? $t('scheduler.page.task.enabled') : $t('scheduler.page.task.disabled') }}
            </Tag>
          </Space>
        </template>
        <template v-if="task.lastRunMessage" #description>
          <div class="text-xs text-gray-600 mt-1">
            {{ $t('scheduler.page.history.lastMessage') }}: {{ task.lastRunMessage }}
          </div>
        </template>
      </Alert>

      <!-- Page-level counts -->
      <Space :size="32">
        <Statistic :title="$t('scheduler.page.history.runsOnPage')" :value="executions.length" />
        <Statistic
          :title="$t('scheduler.page.history.succeeded')"
          :value="successCount"
          :value-style="{ color: '#52C41A' }"
        />
        <Statistic
          :title="$t('scheduler.page.history.failed')"
          :value="failedCount"
          :value-style="{ color: failedCount > 0 ? '#FF4D4F' : undefined }"
        />
        <Statistic
          :title="$t('scheduler.page.history.totalRuns')"
          :value="total"
        />
      </Space>

      <!-- Controls -->
      <Space>
        <RadioGroup v-model:value="showFailedOnly" button-style="solid">
          <Radio :value="false">{{ $t('scheduler.page.history.showAll') }}</Radio>
          <Radio :value="true">{{ $t('scheduler.page.history.showFailedOnly') }}</Radio>
        </RadioGroup>
        <Button @click="refresh">{{ $t('ui.button.refresh') }}</Button>
      </Space>

      <Spin :spinning="loading">
        <Empty
          v-if="!loading && filtered.length === 0"
          :description="
            showFailedOnly
              ? $t('scheduler.page.history.noFailures')
              : $t('scheduler.page.history.noRuns')
          "
        />

        <!-- Each execution rendered as a row card. Failed rows get a
             red left border so a scan of the list shows the
             interesting ones immediately. -->
        <div v-else class="flex flex-col gap-2">
          <div
            v-for="exec in filtered"
            :key="exec.executionId"
            :style="{
              borderLeft: `3px solid ${statusColor(exec.status)}`,
              padding: '8px 12px',
              background: exec.status === 'failed' ? 'rgba(255, 77, 79, 0.06)' : 'rgba(0, 0, 0, 0.02)',
              borderRadius: '4px',
            }"
          >
            <div class="flex justify-between items-start">
              <Space :size="8" wrap>
                <Tag :color="statusColor(exec.status)">{{ exec.status }}</Tag>
                <Typography.Text>{{ formatTimestamp(exec.startedAt) }}</Typography.Text>
                <Tag v-if="exec.attempt && exec.attempt > 0" color="default">
                  {{ $t('scheduler.page.history.attempt') }} {{ exec.attempt }}
                </Tag>
                <Tag color="default">{{ formatDuration(exec.durationMs) }}</Tag>
              </Space>
              <Tooltip :title="exec.executionId">
                <Typography.Text type="secondary" copyable style="font-family: monospace; font-size: 11px">
                  {{ (exec.executionId || '').slice(0, 8) }}…
                </Typography.Text>
              </Tooltip>
            </div>
            <!-- The error message is the part operators actually need.
                 For failures we show it prominently; for success we
                 still show non-empty messages but quietly. -->
            <div
              v-if="exec.message"
              :style="{
                marginTop: '6px',
                fontSize: '12px',
                fontFamily: 'monospace',
                whiteSpace: 'pre-wrap',
                wordBreak: 'break-word',
                color: exec.status === 'failed' ? '#ff4d4f' : '#666',
              }"
            >
              {{ exec.message }}
            </div>
          </div>
        </div>

        <Pagination
          v-if="total > 0"
          v-model:current="page"
          v-model:pageSize="pageSize"
          :total="total"
          :show-size-changer="true"
          :page-size-options="['10', '20', '50', '100']"
          class="mt-3"
        />
      </Spin>
    </div>
  </Drawer>
</template>
