import type { RouteRecordRaw } from 'vue-router'
import '@/main.css'

// Routes mounted by the platform shell under their own error boundary.
export const routes: RouteRecordRaw[] = [
  { path: '/scheduler', name: 'scheduler-tasks', component: () => import('@/views/tasks/index.vue'), meta: { module: 'scheduler' } },
  { path: '/scheduler/dashboard', name: 'scheduler-dashboard', component: () => import('@/views/overview/index.vue'), meta: { module: 'scheduler' } },
  // Former path of the dashboard (4.0.0): keep bookmarks working.
  { path: '/scheduler/overview', name: 'scheduler-overview', redirect: '/scheduler/dashboard', meta: { module: 'scheduler' } },
]
export default routes
