<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth'

const auth = useAuthStore()
const router = useRouter()
const current = computed(() => router.currentRoute.value.name)

async function logout() {
  await auth.logout()
  await router.push({ name: 'login' })
}
</script>

<template>
  <div class="dashboard-shell">
    <aside class="sidebar">
      <div class="brand-mark">DDBOT-AI</div>
      <p class="brand-caption">Platform Foundation</p>
      <nav class="nav-list" aria-label="主导航">
        <RouterLink :class="['nav-item', { active: current === 'overview' }]" to="/overview">Overview</RouterLink>
        <RouterLink :class="['nav-item', { active: current === 'observations' }]" to="/observations">Observations</RouterLink>
        <RouterLink :class="['nav-item', { active: current === 'sources' }]" to="/sources">Sources</RouterLink>
        <RouterLink :class="['nav-item', { active: current === 'targets' }]" to="/targets">Targets</RouterLink>
        <RouterLink :class="['nav-item', { active: current === 'connectors' }]" to="/connectors">Connectors</RouterLink>
        <RouterLink :class="['nav-item', { active: current === 'ai' }]" to="/ai">AI Shadow</RouterLink>
        <RouterLink :class="['nav-item', { active: current === 'about' }]" to="/about">About</RouterLink>
      </nav>
    </aside>
    <div class="main-column">
      <header class="topbar">
        <span class="topbar-title">DDBOT-AI</span>
        <div class="topbar-actions">
          <span class="user-name">{{ auth.username }}</span>
          <el-button text @click="logout">退出登录</el-button>
        </div>
      </header>
      <main class="content-area">
        <slot />
      </main>
    </div>
  </div>
</template>
