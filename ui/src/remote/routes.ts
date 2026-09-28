import type { RouteRecordRaw } from 'vue-router'
import '@/main.css'

// Routes mounted by the platform shell under their own error boundary.
export const routes: RouteRecordRaw[] = [
  { path: '/scheduler', name: 'scheduler-tasks', component: () => import('@/views/tasks/index.vue'), meta: { module: 'scheduler' } },
  { path: '/scheduler/overview', name: 'scheduler-overview', component: () => import('@/views/overview/index.vue'), meta: { module: 'scheduler' } },
]
export default routes
