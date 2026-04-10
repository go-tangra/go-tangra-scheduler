import type { TangraModule } from './sdk';

import routes from './routes';
import { useSchedulerTaskStore } from './stores/scheduler-task.state';
import enUS from './locales/en-US.json';

const schedulerModule: TangraModule = {
  id: 'scheduler',
  version: '1.0.0',
  routes,
  stores: {
    'scheduler-task': useSchedulerTaskStore,
  },
  locales: {
    'en-US': enUS,
  },
};

export default schedulerModule;
