import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '../stores/auth'
import SetupView from '../views/SetupView.vue'
import LoginView from '../views/LoginView.vue'
import OverviewView from '../views/OverviewView.vue'
import AboutView from '../views/AboutView.vue'
import ObservationsView from '../views/ObservationsView.vue'
import SourcesView from '../views/SourcesView.vue'
import TargetsView from '../views/TargetsView.vue'
import ConnectorsView from '../views/ConnectorsView.vue'

const router = createRouter({
  history: createWebHistory('/'),
  routes: [
    { path: '/', redirect: '/overview' },
    { path: '/setup', name: 'setup', component: SetupView },
    { path: '/login', name: 'login', component: LoginView },
    { path: '/overview', name: 'overview', component: OverviewView, meta: { requiresAuth: true } },
    { path: '/observations', name: 'observations', component: ObservationsView, meta: { requiresAuth: true } },
    { path: '/sources', name: 'sources', component: SourcesView, meta: { requiresAuth: true } },
    { path: '/targets', name: 'targets', component: TargetsView, meta: { requiresAuth: true } },
    { path: '/connectors', name: 'connectors', component: ConnectorsView, meta: { requiresAuth: true } },
    { path: '/about', name: 'about', component: AboutView, meta: { requiresAuth: true } },
  ],
})

let bootstrapped = false
router.beforeEach(async (to) => {
  const auth = useAuthStore()
  if (!bootstrapped) {
    await auth.bootstrap()
    bootstrapped = true
  }
  if (auth.status === 'setup_required' && to.name !== 'setup') return { name: 'setup' }
  if (to.name === 'setup' && auth.status !== 'setup_required') return { name: auth.status === 'authenticated' ? 'overview' : 'login' }
  if (to.name === 'login' && auth.status === 'authenticated') return { name: 'overview' }
  if (to.meta.requiresAuth && auth.status !== 'authenticated') return { name: 'login', query: { next: to.fullPath } }
  return true
})

export default router
