<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { get, displayError, isUnauthorized } from '../api/client'
import type { Overview, ComponentStatus } from '../api/types'
import DashboardLayout from '../components/DashboardLayout.vue'
import { useAuthStore } from '../stores/auth'
import { useRouter } from 'vue-router'

const overview = ref<Overview | null>(null)
const error = ref('')
const loading = ref(true)
const auth = useAuthStore()
const router = useRouter()
const statusItems = computed(() => {
  if (!overview.value) return []
  return [
    { name: 'Legacy Core', value: overview.value.platform.legacy_core },
    { name: 'Admin API', value: overview.value.platform.admin_api },
    { name: 'SQLite', value: overview.value.platform.sqlite },
    { name: 'Secret Store', value: overview.value.platform.secret_store },
    { name: 'Auth', value: overview.value.platform.auth },
  ]
})

function statusLabel(status: ComponentStatus['status']) {
  return { available: '正常', degraded: '降级', recovery: 'Recovery', unavailable: '不可用', unknown: '未知' }[status]
}

async function load() {
  loading.value = true
  try {
    overview.value = await get<Overview>('/api/v2/platform/overview')
    error.value = ''
  } catch (err) {
    if (isUnauthorized(err)) {
      auth.clear()
      auth.status = 'unauthenticated'
      await router.replace({ name: 'login' })
      return
    }
    error.value = displayError(err)
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <DashboardLayout>
    <div class="page-heading">
      <div>
        <p class="eyebrow">PLATFORM FOUNDATION</p>
        <h1>系统总览</h1>
        <p>当前阶段提供采集基础设施、认证和安全存储状态；业务管理模块将在后续阶段启用。</p>
      </div>
      <el-button :loading="loading" @click="load">刷新</el-button>
    </div>
    <el-alert v-if="error" type="error" :title="error" show-icon :closable="false" />
    <el-skeleton v-if="loading && !overview" :rows="5" animated />
    <template v-else-if="overview">
      <el-card class="product-card" shadow="never">
        <div>
          <span class="card-kicker">PRODUCT</span>
          <h2>{{ overview.product.name }}</h2>
        </div>
        <div class="build-values">
          <span>Version {{ overview.product.version }}</span>
          <span>Commit {{ overview.product.commit }}</span>
        </div>
      </el-card>
      <div class="status-grid">
        <el-card v-for="item in statusItems" :key="item.name" class="status-card" shadow="never">
          <div class="status-card-head">
            <span>{{ item.name }}</span>
            <el-tag :type="item.value.status === 'available' ? 'success' : item.value.status === 'recovery' ? 'warning' : item.value.status === 'unknown' ? 'info' : 'danger'" effect="light">
              {{ statusLabel(item.value.status) }}
            </el-tag>
          </div>
          <code>{{ item.value.code || 'status_unknown' }}</code>
        </el-card>
      </div>
    </template>
  </DashboardLayout>
</template>
