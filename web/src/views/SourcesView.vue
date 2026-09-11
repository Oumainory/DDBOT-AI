<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { command, displayError, get, isUnauthorized, post } from '../api/client'
import type { Source, SubscriptionItem, Target } from '../api/types'
import DashboardLayout from '../components/DashboardLayout.vue'
import { useAuthStore } from '../stores/auth'
import { useRouter } from 'vue-router'

const auth = useAuthStore()
const router = useRouter()
const sources = ref<Source[]>([])
const targets = ref<Target[]>([])
const selected = ref<Source | null>(null)
const relations = ref<SubscriptionItem[]>([])
const detailOpen = ref(false)
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const form = reactive({ platform: 'bilibili', external_id: '', display_name: '' })
const searchQuery = ref('')
const searchResults = ref<Array<{ uid: string; name: string; profile_url: string }>>([])
const searchNotice = ref('')
const searching = ref(false)

function handleError(value: unknown) {
  if (isUnauthorized(value)) {
    auth.clear(); auth.status = 'unauthenticated'; void router.replace({ name: 'login' }); return
  }
  error.value = displayError(value)
}

async function load() {
  loading.value = true; error.value = ''
  try {
    const [data, targetData] = await Promise.all([
      get<{ items: Source[] }>('/api/v2/sources'),
      get<{ items: Target[] }>('/api/v2/targets'),
    ])
    sources.value = data.items ?? []
    targets.value = targetData.items ?? []
  } catch (value) { handleError(value) } finally { loading.value = false }
}

async function resolveAndCreate() {
  if (!form.external_id.trim()) { error.value = '请输入 UID 或官方 profile URL'; return }
  saving.value = true; error.value = ''
  try {
    let body: Record<string, string> = { ...form }
    if (form.platform === 'twitter') {
      const resolved = await post<Source>('/api/v2/discovery/twitter/resolve', { value: form.external_id })
      body = { platform: 'twitter', external_id: resolved.external_id, display_name: form.display_name || resolved.display_name || '', profile_url: resolved.canonical_url || '' }
    }
    await command('/api/v2/sources', 'POST', body, auth.csrfToken)
    form.external_id = ''; form.display_name = ''
    await load()
  } catch (value) { handleError(value) } finally { saving.value = false }
}

async function searchBilibili() {
  if (!searchQuery.value.trim()) return
  searching.value = true; searchNotice.value = ''; searchResults.value = []
  try {
    const data = await post<{ items: Array<{ uid: string; name: string; profile_url: string }> }>('/api/v2/discovery/bilibili/search', { query: searchQuery.value })
    searchResults.value = data.items ?? []
  } catch (value) {
    handleError(value)
    searchNotice.value = '搜索不可用；仍可在下方使用 UID / 官方 profile URL 直接添加。'
  } finally { searching.value = false }
}

function chooseCandidate(candidate: { uid: string; name: string }) {
  form.external_id = candidate.uid; form.display_name = candidate.name; searchNotice.value = ''
}

async function remove(source: Source) {
  try { await command(`/api/v2/sources/${encodeURIComponent(source.id)}`, 'DELETE', {}, auth.csrfToken); await load() } catch (value) { handleError(value) }
}

async function openDetail(source: Source) {
  selected.value = source; detailOpen.value = true; error.value = ''
  try {
    const data = await get<{ items: SubscriptionItem[] }>(`/api/v2/sources/${encodeURIComponent(source.id)}/targets`)
    relations.value = data.items ?? []
  } catch (value) { handleError(value) }
}

async function addSubscription(targetId: string) {
  if (!selected.value || !targetId) return
  try {
    await command('/api/v2/subscriptions', 'POST', { source_id: selected.value.id, target_id: targetId, type: 'dynamic' }, auth.csrfToken)
    await openDetail(selected.value); await load()
  } catch (value) { handleError(value) }
}

async function removeSubscription(item: SubscriptionItem) {
  try {
    await command(`/api/v2/subscriptions/${encodeURIComponent(item.subscription.id)}`, 'DELETE', {}, auth.csrfToken)
    if (selected.value) await openDetail(selected.value)
    await load()
  } catch (value) { handleError(value) }
}

onMounted(load)
</script>

<template>
  <DashboardLayout>
    <div class="page-heading"><div><p class="eyebrow">SOURCES</p><h1>监控来源</h1><p>用官方身份解析结果建立 Source；平台采集仍由 Legacy Concern 负责。</p></div><el-button :loading="loading" @click="load">刷新</el-button></div>
    <el-alert v-if="error" type="error" :title="error" show-icon :closable="false" />
    <el-card v-if="form.platform === 'bilibili'" class="domain-card" shadow="never"><el-form :inline="true" @submit.prevent="searchBilibili"><el-form-item label="Bilibili 搜索"><el-input v-model="searchQuery" placeholder="名称或 UID" /></el-form-item><el-form-item><el-button :loading="searching" @click="searchBilibili">搜索</el-button></el-form-item></el-form><el-alert v-if="searchNotice" type="warning" :title="searchNotice" :closable="false" /><div v-if="searchResults.length" class="candidate-list"><el-button v-for="candidate in searchResults" :key="candidate.uid" text @click="chooseCandidate(candidate)">{{ candidate.name || candidate.uid }} · {{ candidate.uid }}</el-button></div></el-card>
    <el-card class="domain-card" shadow="never">
      <el-form :inline="true" @submit.prevent="resolveAndCreate">
        <el-form-item label="平台"><el-select v-model="form.platform" style="width: 140px"><el-option label="Bilibili" value="bilibili" /><el-option label="Twitter / X" value="twitter" /></el-select></el-form-item>
        <el-form-item label="UID / Handle / URL"><el-input v-model="form.external_id" placeholder="401742377 或 @GenshinImpact" /></el-form-item>
        <el-form-item label="显示名"><el-input v-model="form.display_name" placeholder="可选" /></el-form-item>
        <el-form-item><el-button type="primary" :loading="saving" @click="resolveAndCreate">添加 Source</el-button></el-form-item>
      </el-form>
    </el-card>
    <el-card class="domain-card" shadow="never">
      <el-table :data="sources" v-loading="loading" row-key="id" @row-click="openDetail">
        <el-table-column prop="platform" label="平台" width="120" />
        <el-table-column label="身份" min-width="220"><template #default="scope"><strong>{{ scope.row.display_name || scope.row.handle || scope.row.external_id }}</strong><br><code>{{ scope.row.external_id }}</code></template></el-table-column>
        <el-table-column prop="status" label="状态" width="130" />
        <el-table-column prop="subscription_count" label="订阅数" width="90" />
        <el-table-column label="操作" width="150"><template #default="scope"><el-button link @click.stop="openDetail(scope.row)">详情</el-button><el-button link type="danger" @click.stop="remove(scope.row)">删除</el-button></template></el-table-column>
      </el-table>
      <el-empty v-if="!loading && !sources.length" description="尚未建立 Source" />
    </el-card>
    <el-drawer v-model="detailOpen" title="Source 详情" size="min(680px, 94vw)">
      <template v-if="selected">
        <div class="detail-meta"><span>{{ selected.platform }} / {{ selected.display_name || selected.external_id }}</span><code>{{ selected.id }}</code><small>{{ selected.status }}</small></div>
        <p>Canonical URL：<a v-if="selected.canonical_url" :href="selected.canonical_url" target="_blank" rel="noreferrer">{{ selected.canonical_url }}</a><span v-else>—</span></p>
        <el-divider content-position="left">添加 Target Subscription</el-divider>
        <el-select placeholder="选择推送目标" style="width: 100%" @change="addSubscription"><el-option v-for="target in targets" :key="target.id" :label="`${target.display_name || target.external_id} · ${target.target_type}`" :value="target.id" /></el-select>
        <el-divider content-position="left">Source → Targets</el-divider>
        <el-empty v-if="!relations.length" description="暂无订阅" />
        <div v-for="item in relations" :key="item.subscription.id" class="relation-row"><span>{{ item.target?.display_name || item.target?.external_id }}</span><code>{{ item.subscription.legacy_key }}</code><el-button link type="danger" @click="removeSubscription(item)">移除</el-button></div>
      </template>
    </el-drawer>
  </DashboardLayout>
</template>
