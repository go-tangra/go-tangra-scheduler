import type { RouteRecordRaw } from 'vue-router';

const routes: RouteRecordRaw[] = [
  {
    path: '/scheduler',
    name: 'Scheduler',
    component: () => import('shell/app-layout'),
    redirect: '/scheduler/tasks',
    meta: {
      order: 2050,
      icon: 'lucide:clock',
      title: 'scheduler.menu.scheduler',
      keepAlive: true,
      authority: ['platform:admin', 'tenant:manager'],
    },
    children: [
      {
        path: 'tasks',
        name: 'SchedulerTasks',
        meta: {
          icon: 'lucide:list-checks',
          title: 'scheduler.menu.tasks',
          authority: ['platform:admin', 'tenant:manager'],
        },
        component: () => import('./views/tasks/index.vue'),
      },
    ],
  },
];

export default routes;
