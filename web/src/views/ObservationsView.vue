<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ApiError, displayError, get, isUnauthorized } from '../api/client'
import type { ObservationDelivery, ObservationEvent, ObservationEventDetail, ObservationPage, ObservationRoute, ObservationSummary } from '../api/types'
import DashboardLayout from '../components/DashboardLayout.vue'
import { useAuthStore } from '../stores/auth'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const events = ref<ObservationEvent[]>([])
const nextCursor = ref('')
const summary = ref<ObservationSummary | null>(null)
const selected = ref<ObservationEventDetail | null>(null)
const drawerOpen = ref(false)
const loading = ref(false)
const detailLoading = ref(false)
const error = ref('')
const unavailable = ref(false)
const filters = reactive({
  platform: String(route.query.platform ?? ''),
  eventType: String(route.query.event_type ?? ''),
  sourceExternalID: String(route.query.source_external_id ?? ''),
  from: String(route.query.from ?? ''),
  to: String(route.query.to ?? ''),
})

const hasEvents = computed(() => events.value.length > 0)

function queryPath(cursor = '') {
  const params = new URLSearchParams()
  params.set('limit', '50')
  if (cursor) params.set('cursor', cursor)
  if (filters.platform) params.set('platform', filters.platform)
  if (filters.eventType) params.set('event_type', filters.eventType)
  if (filters.sourceExternalID) params.set('source_external_id', filters.sourceExternalID)
  if (filters.from) params.set('from', filters.from)
  if (filters.to) params.set('to', filters.to)
  return `/api/v2/observations/events?${params.toString()}`
}

async function syncURL() {
  await router.replace({ query: {
    ...(filters.platform ? { platform: filters.platform } : {}),
    ...(filters.eventType ? { event_type: filters.eventType } : {}),
    ...(filters.sourceExternalID ? { source_external_id: filters.sourceExternalID } : {}),
    ...(filters.from ? { from: filters.from } : {}),
    ...(filters.to ? { to: filters.to } : {}),
  } })
}

function handleError(value: unknown) {
  if (isUnauthorized(value)) {
    auth.clear()
    auth.status = 'unauthenticated'
    void router.replace({ name: 'login', query: { next: route.fullPath } })
    return
  }
  unavailable.value = value instanceof ApiError && value.status === 503
  error.value = displayError(value)
}

async function loadSummary() {
  try {
    summary.value = await get<ObservationSummary>('/api/v2/observations/summary')
  } catch (value) {
    handleError(value)
  }
}

async function loadEvents(append = false) {
  loading.value = true
  error.value = ''
  if (!append) {
    events.value = []
    nextCursor.value = ''
  }
  try {
    const page = await get<ObservationPage>(queryPath(append ? nextCursor.value : ''))
    events.value = append ? [...events.value, ...page.items] : page.items
    nextCursor.value = page.next_cursor ?? ''
    unavailable.value = false
  } catch (value) {
    handleError(value)
  } finally {
    loading.value = false
  }
}

async function applyFilters() {
  await syncURL()
  await Promise.all([loadEvents(), loadSummary()])
}

async function openDetail(event: ObservationEvent) {
	selected.value = null
	drawerOpen.value = true
  detailLoading.value = true
  try {
    selected.value = await get<ObservationEventDetail>(`/api/v2/observations/events/${encodeURIComponent(event.id)}`)
  } catch (value) {
    handleError(value)
  } finally {
    detailLoading.value = false
  }
}

function formatDate(value: string) {
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'medium' }).format(new Date(value))
}

function routeLabel(value: ObservationRoute['outcome']) {
  return { pass: '通过', filtered: '已过滤', skipped: '已跳过', unknown: '未知' }[value]
}

function deliveryLabel(value: ObservationDelivery['status']) {
  return { sent: '已发送', queued: '已入队', not_sent: '未发送', rejected: '已拒绝', unknown: '结果未知（不会自动重试）' }[value]
}

function tagType(value: string) {
  if (value === 'sent' || value === 'pass') return 'success'
  if (value === 'filtered' || value === 'not_sent') return 'info'
  if (value === 'unknown') return 'warning'
  return 'danger'
}

onMounted(() => {
  void Promise.all([loadEvents(), loadSummary()])
})
</script>

<template>
  <DashboardLayout>
    <div class="page-heading">
      <div>
        <p class="eyebrow">OBSERVATIONS</p>
        <h1>运行观察</h1>
        <p>查看 Legacy 事件、路由和实际投递结果。这里展示的是旁路事实，不会改变原有发送行为。</p>
      </div>
      <el-button :loading="loading" @click="applyFilters">刷新</el-button>
    </div>

    <el-alert v-if="unavailable" type="warning" title="Observation 当前不可用/未启用" show-icon :closable="false" />
    <el-alert v-else-if="error" type="error" :title="error" show-icon :closable="false" />

    <div class="observation-summary-grid">
      <el-card shadow="never"><span class="card-kicker">RUNTIME</span><strong>{{ summary?.runtime.status ?? '未知' }}</strong><small>队列 {{ summary?.runtime.queue_depth ?? 0 }} / {{ summary?.runtime.queue_capacity ?? 0 }}</small></el-card>
      <el-card shadow="never"><span class="card-kicker">24H EVENTS</span><strong>{{ summary?.recent.events_24h ?? 0 }}</strong><small>进程接受 {{ summary?.runtime.events_accepted ?? 0 }}</small></el-card>
      <el-card shadow="never"><span class="card-kicker">24H DELIVERIES</span><strong>{{ summary?.recent.deliveries_24h ?? 0 }}</strong><small>保留 {{ summary?.window.retention_days ?? 90 }} 天</small></el-card>
    </div>

    <el-card class="observation-filter-card" shadow="never">
      <el-form :inline="true" @submit.prevent="applyFilters">
        <el-form-item label="平台"><el-input v-model="filters.platform" placeholder="bilibili / twitter" clearable /></el-form-item>
        <el-form-item label="事件类型"><el-input v-model="filters.eventType" placeholder="dynamic / tweet" clearable /></el-form-item>
        <el-form-item label="Source ID"><el-input v-model="filters.sourceExternalID" placeholder="高级筛选" clearable /></el-form-item>
        <el-form-item label="From"><el-input v-model="filters.from" placeholder="RFC3339" clearable /></el-form-item>
        <el-form-item label="To"><el-input v-model="filters.to" placeholder="RFC3339" clearable /></el-form-item>
        <el-form-item><el-button type="primary" @click="applyFilters">应用筛选</el-button></el-form-item>
      </el-form>
    </el-card>

    <el-card class="observation-list-card" shadow="never">
      <el-skeleton v-if="loading && !hasEvents" :rows="6" animated />
      <el-empty v-else-if="!hasEvents" description="暂无 Observation 记录" />
      <el-table v-else :data="events" row-key="id" @row-click="openDetail">
        <el-table-column label="观察时间" min-width="180"><template #default="scope">{{ formatDate(scope.row.observed_at) }}</template></el-table-column>
        <el-table-column prop="platform" label="平台" width="110" />
        <el-table-column label="Source" min-width="150"><template #default="scope">{{ scope.row.source_kind }} / {{ scope.row.source_external_id || '—' }}</template></el-table-column>
        <el-table-column prop="event_type" label="事件" width="120" />
        <el-table-column label="预览" min-width="260"><template #default="scope"><span class="observation-preview">{{ scope.row.public_summary.text || scope.row.public_summary.url || '（无文本预览）' }}</span></template></el-table-column>
        <el-table-column label="Route" width="100"><template #default="scope">{{ scope.row.route_count ?? 0 }}</template></el-table-column>
        <el-table-column label="Delivery" min-width="150"><template #default="scope"><el-tag v-if="scope.row.final_delivery_status" :type="tagType(scope.row.final_delivery_status)">{{ deliveryLabel(scope.row.final_delivery_status) }}</el-tag><span v-else>—</span></template></el-table-column>
      </el-table>
      <div v-if="nextCursor" class="observation-pagination"><el-button :loading="loading" @click="loadEvents(true)">加载下一批</el-button></div>
    </el-card>

	<el-drawer v-model="drawerOpen" title="Observation 详情" size="min(720px, 96vw)">
      <el-skeleton v-if="detailLoading" :rows="8" animated />
      <template v-else-if="selected">
        <div class="detail-meta"><span>{{ selected.event.platform }} / {{ selected.event.event_type }}</span><code>{{ selected.event.id }}</code><small>{{ formatDate(selected.event.observed_at) }}</small></div>
        <el-card shadow="never" class="snapshot-card"><span class="card-kicker">PUBLIC SNAPSHOT</span><h3>{{ selected.event.public_snapshot.author_name || selected.event.public_snapshot.author_id || '未知作者' }}</h3><p>{{ selected.event.public_snapshot.text || '（无文本内容）' }}</p><a v-if="selected.event.public_snapshot.url" :href="selected.event.public_snapshot.url" target="_blank" rel="noreferrer">打开公开链接</a><div v-if="selected.event.public_snapshot.media_urls?.length" class="media-list"><a v-for="url in selected.event.public_snapshot.media_urls" :key="url" :href="url" target="_blank" rel="noreferrer">媒体链接</a></div></el-card>
        <h3 class="timeline-heading">Route timeline</h3>
        <el-empty v-if="!selected.routes.length" description="没有 Route 记录" />
        <div v-for="routeItem in selected.routes" :key="routeItem.id" class="timeline-row"><el-tag :type="tagType(routeItem.outcome)">{{ routeLabel(routeItem.outcome) }}</el-tag><span>{{ routeItem.destination_kind }} / {{ routeItem.destination_external_id }}</span><code>{{ routeItem.reason_code }}</code><small>{{ formatDate(routeItem.observed_at) }}</small></div>
        <h3 class="timeline-heading">Delivery timeline</h3>
        <el-empty v-if="!selected.deliveries.length" description="没有 Delivery 记录" />
        <div v-for="delivery in selected.deliveries" :key="delivery.id" class="timeline-row"><el-tag :type="tagType(delivery.status)">{{ deliveryLabel(delivery.status) }}</el-tag><span>{{ delivery.connector_kind }} / {{ delivery.destination_external_id }}</span><code>{{ delivery.result_code }}</code><small>{{ formatDate(delivery.observed_at) }}</small></div>
      </template>
    </el-drawer>
  </DashboardLayout>
</template>
