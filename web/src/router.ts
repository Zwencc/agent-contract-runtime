import { createRouter, createWebHistory } from 'vue-router'
import AppShell from './layouts/AppShell.vue'

const router = createRouter({
  history: createWebHistory(),
  routes: [{
    path: '/', component: AppShell, children: [
      { path: '', name: 'overview', component: () => import('./views/OverviewView.vue') },
      { path: 'sessions', name: 'sessions', component: () => import('./views/SessionsView.vue') },
      { path: 'replay/:session?', name: 'replay', component: () => import('./views/ReplayView.vue') },
      { path: 'contracts', name: 'contracts', component: () => import('./views/ContractsView.vue') },
      { path: 'memories', name: 'memories', component: () => import('./views/MemoriesView.vue') },
      { path: 'graph', name: 'graph', component: () => import('./views/GraphView.vue') },
      { path: 'insights/agents', name: 'agents', component: () => import('./views/AgentComparisonView.vue') },
      { path: 'insights/failures', name: 'failures', component: () => import('./views/FailuresView.vue') },
      { path: 'insights/reliance', name: 'reliance', component: () => import('./views/RelianceView.vue') },
      { path: 'insights/interaction', name: 'interaction', component: () => import('./views/InteractionView.vue') },
      { path: 'integrity', name: 'integrity', component: () => import('./views/IntegrityView.vue') },
      { path: 'settings', name: 'settings', component: () => import('./views/SettingsView.vue') }
    ]
  }]
})

export default router
