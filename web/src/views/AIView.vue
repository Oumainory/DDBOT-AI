<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { command, displayError, get, isUnauthorized, post } from '../api/client'
import type { AIProvider, AIProfile, AIDecision, AIShadowSummary, AIPolicy, AIEvaluationRun, AIEvaluationCase, Phase5Readiness, Phase5RouteDecision, Phase5Delivery, MediaCacheSummary, Source, Target, SubscriptionItem } from '../api/types'
import { policyScopeOptions } from '../api/policyScopes'
import DashboardLayout from '../components/DashboardLayout.vue'
import { useAuthStore } from '../stores/auth'
import { useRouter } from 'vue-router'

const auth = useAuthStore()
const router = useRouter()
const provider = ref<AIProvider | null>(null)
const profiles = ref<AIProfile[]>([])
const summary = ref<AIShadowSummary | null>(null)
const decisions = ref<AIDecision[]>([])
const runs = ref<AIEvaluationRun[]>([])
const cases = ref<AIEvaluationCase[]>([])
const policy = ref<AIPolicy>({ scope_type: 'global', scope_id: '', mode: 'shadow', default_action: 'pass', threshold: 0.9 })
const error = ref('')
const loading = ref(true)
const saving = ref(false)
const testing = ref(false)
const form = ref({ base_url: '', model: '', structured_output_mode: 'json_schema', api_key: '' })
const caseForm = ref({ snapshot: '', expected_importance: '', expected_action: '', critical: false, label_kind: 'synthetic', notes: '', source: 'dashboard' })
const profileDialogVisible = ref(false)
const profileSaving = ref(false)
const editingProfile = ref<AIProfile | null>(null)
const profileForm = ref({
  name: '',
  description: '',
  default_action: 'pass' as AIProfile['default_action'],
  category_actions: '{}',
  tag_actions: '{}',
  safety: '{}',
})
const policyScope = ref<'global' | 'source' | 'target' | 'subscription'>('global')
const policyScopeID = ref('')
const sources = ref<Source[]>([])
const targets = ref<Target[]>([])
const subscriptions = ref<SubscriptionItem[]>([])
const policyLoading = ref(false)
const readiness = ref<Phase5Readiness | null>(null)
const emergencyDisabled = ref(false)
const routeDecisions = ref<Phase5RouteDecision[]>([])
const deliveries = ref<Phase5Delivery[]>([])
const mediaSummary = ref<MediaCacheSummary | null>(null)
const phase5Busy = ref(false)

async function load() {
  loading.value = true
  try {
    const [providerData, profileData, summaryData, decisionData, policyData, runData, caseData, readinessData, routeData, deliveryData, mediaData] = await Promise.all([
      get<{ provider?: AIProvider }>('/api/v2/ai/provider'),
      get<{ items: AIProfile[] }>('/api/v2/ai/profiles'),
      get<{ summary: AIShadowSummary }>('/api/v2/ai/shadow/summary'),
      get<{ items: AIDecision[] }>('/api/v2/ai/shadow/decisions?limit=20'),
      get<AIPolicy>('/api/v2/ai/policy/global'),
      get<{ items: AIEvaluationRun[] }>('/api/v2/ai/evaluation/runs'),
      get<{ items: AIEvaluationCase[] }>('/api/v2/ai/evaluation/cases'),
      get<{ readiness: Phase5Readiness; emergency_disabled?: boolean }>('/api/v2/ai/enforce/readiness'),
      get<{ items: Phase5RouteDecision[] }>('/api/v2/route-decisions?limit=20'),
      get<{ items: Phase5Delivery[] }>('/api/v2/deliveries?limit=20'),
      get<MediaCacheSummary>('/api/v2/media-cache/summary'),
    ])
    provider.value = providerData.provider ?? null
    if (provider.value) {
      form.value.base_url = provider.value.base_url
      form.value.model = provider.value.model
      form.value.structured_output_mode = provider.value.structured_output_mode
    }
    profiles.value = profileData.items ?? []
    summary.value = summaryData.summary ?? null
    decisions.value = decisionData.items ?? []
    policy.value = policyData ?? policy.value
    runs.value = runData.items ?? []
    cases.value = caseData.items ?? []
    readiness.value = readinessData.readiness ?? null
    emergencyDisabled.value = Boolean(readinessData.emergency_disabled)
    routeDecisions.value = routeData.items ?? []
    deliveries.value = deliveryData.items ?? []
    mediaSummary.value = mediaData ?? null
    await loadPolicyScopes()
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

const selectedPolicyScopeOptions = computed(() => {
  if (policyScope.value === 'global') return []
  return policyScopeOptions(policyScope.value, sources.value, targets.value, subscriptions.value)
})

async function loadPolicyScopes() {
  const [sourceResult, targetResult, subscriptionResult] = await Promise.allSettled([
    get<{ items: Source[] }>('/api/v2/sources'),
    get<{ items: Target[] }>('/api/v2/targets'),
    get<{ items: SubscriptionItem[] }>('/api/v2/subscriptions'),
  ])
  if (sourceResult.status === 'fulfilled') sources.value = sourceResult.value.items ?? []
  if (targetResult.status === 'fulfilled') targets.value = targetResult.value.items ?? []
  if (subscriptionResult.status === 'fulfilled') subscriptions.value = subscriptionResult.value.items ?? []
}

function onPolicyScopeChange() {
  policyScopeID.value = ''
  policy.value = { scope_type: policyScope.value, scope_id: '', mode: 'inherit' }
  const first = selectedPolicyScopeOptions.value[0]
  if (first) {
    policyScopeID.value = first.id
    void loadPolicyScope()
  }
}

async function enforceCommand(path: string, body: unknown = {}) {
  phase5Busy.value = true
  try {
    await command(path, 'POST', body, auth.csrfToken)
    await load()
  } catch (err) {
    error.value = displayError(err)
  } finally {
    phase5Busy.value = false
  }
}

function approveEnforce() { return enforceCommand('/api/v2/ai/enforce/approve', {}) }
function revokeEnforce() { return enforceCommand('/api/v2/ai/enforce/revoke', {}) }
function disableEmergency() { return enforceCommand('/api/v2/ai/enforce/emergency-disable', {}) }
function enableEmergency() { return enforceCommand('/api/v2/ai/enforce/emergency-enable', {}) }

async function replayDecision(item: Phase5RouteDecision) {
  await enforceCommand(`/api/v2/route-decisions/${encodeURIComponent(item.id)}/replay`, {})
}

function openProfile(profile?: AIProfile) {
  // Built-in profiles are immutable by contract; they can still be cloned.
  if (profile?.builtin) return
  editingProfile.value = profile ?? null
  profileForm.value = {
    name: profile?.name ?? '',
    description: profile?.description ?? '',
    default_action: profile?.default_action ?? 'pass',
    category_actions: JSON.stringify(profile?.category_actions ?? {}, null, 2),
    tag_actions: JSON.stringify(profile?.tag_actions ?? {}, null, 2),
    safety: JSON.stringify(profile?.safety ?? {}, null, 2),
  }
  profileDialogVisible.value = true
}

function parseObject(value: string, label: string): Record<string, unknown> {
  if (!value.trim()) return {}
  const parsed: unknown = JSON.parse(value)
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
    throw new Error(`${label} 必须是 JSON 对象`)
  }
  return parsed as Record<string, unknown>
}

async function saveProfile() {
  profileSaving.value = true
  try {
    const body = {
      name: profileForm.value.name.trim(),
      description: profileForm.value.description,
      default_action: profileForm.value.default_action,
      category_actions: parseObject(profileForm.value.category_actions, 'category_actions'),
      tag_actions: parseObject(profileForm.value.tag_actions, 'tag_actions'),
      safety: parseObject(profileForm.value.safety, 'safety'),
    }
    const path = editingProfile.value
      ? `/api/v2/ai/profiles/${encodeURIComponent(editingProfile.value.id)}`
      : '/api/v2/ai/profiles'
    await command(path, editingProfile.value ? 'PATCH' : 'POST', body, auth.csrfToken)
    profileDialogVisible.value = false
    await load()
  } catch (err) {
    if (err instanceof SyntaxError || err instanceof Error && !(err as { code?: string }).code) {
      error.value = err instanceof Error ? err.message : 'Profile JSON 无效'
    } else {
      error.value = displayError(err)
    }
  } finally {
    profileSaving.value = false
  }
}

async function reviewDecision(decision: AIDecision) {
  try {
    await command(`/api/v2/ai/shadow/decisions/${encodeURIComponent(decision.id)}/review`, 'POST', { reviewed: true }, auth.csrfToken)
    await load()
  } catch (err) {
    error.value = displayError(err)
  }
}

async function cloneProfile(profile: AIProfile) {
  try {
    await command('/api/v2/ai/profiles', 'POST', { name: `${profile.name} Copy`, description: profile.description ?? '', default_action: profile.default_action, category_actions: profile.category_actions ?? {}, tag_actions: profile.tag_actions ?? {}, safety: profile.safety ?? {} }, auth.csrfToken)
    await load()
  } catch (err) {
    error.value = displayError(err)
  }
}

async function deleteProfile(profile: AIProfile) {
  if (profile.builtin) return
  try {
    await command(`/api/v2/ai/profiles/${encodeURIComponent(profile.id)}`, 'DELETE', {}, auth.csrfToken)
    await load()
  } catch (err) {
    error.value = displayError(err)
  }
}

async function createCase() {
  try {
    const snapshot = JSON.parse(caseForm.value.snapshot)
    await command('/api/v2/ai/evaluation/cases', 'POST', { normalized_input_snapshot: snapshot, expected_importance: caseForm.value.expected_importance, expected_action: caseForm.value.expected_action, critical: caseForm.value.critical, label_kind: caseForm.value.label_kind, notes: caseForm.value.notes, source: caseForm.value.source }, auth.csrfToken)
    caseForm.value.snapshot = ''
    await load()
  } catch (err) {
    error.value = err instanceof SyntaxError ? 'NormalizedEvent JSON 无效' : displayError(err)
  }
}

async function deleteCase(item: AIEvaluationCase) {
  try {
    await command(`/api/v2/ai/evaluation/cases/${encodeURIComponent(item.id)}`, 'DELETE', {}, auth.csrfToken)
    await load()
  } catch (err) {
    error.value = displayError(err)
  }
}

async function exportCases() {
  try {
    const data = await get<{ items: AIEvaluationCase[] }>('/api/v2/ai/evaluation/cases/export')
    const blob = new Blob([JSON.stringify(data.items ?? [], null, 2)], { type: 'application/json' })
    const link = document.createElement('a')
    link.href = URL.createObjectURL(blob)
    link.download = 'ddbot-ai-evaluation-cases.json'
    link.click()
    URL.revokeObjectURL(link.href)
  } catch (err) {
    error.value = displayError(err)
  }
}

function policyPath(): string | null {
  if (policyScope.value === 'global') return '/api/v2/ai/policy/global'
  const id = policyScopeID.value.trim()
  if (!id) return null
  return `/api/v2/ai/policy/${policyScope.value}/${encodeURIComponent(id)}`
}

async function loadPolicyScope() {
  const path = policyPath()
  if (!path) {
    policy.value = { scope_type: policyScope.value, scope_id: policyScopeID.value.trim(), mode: 'inherit' }
    return
  }
  policyLoading.value = true
  try {
    policy.value = await get<AIPolicy>(path)
    error.value = ''
  } catch (err) {
    error.value = displayError(err)
  } finally {
    policyLoading.value = false
  }
}

async function savePolicy() {
  try {
    const path = policyPath()
    if (!path) {
      error.value = '请选择一个真实的 Source / Target / Subscription'
      return
    }
    await command(path, 'PATCH', {
      mode: policy.value.mode,
      profile_id: policy.value.profile_id ?? '',
      threshold: policy.value.threshold,
      default_action: policy.value.default_action ?? 'inherit',
      category_actions: policy.value.category_actions ?? {},
      tag_actions: policy.value.tag_actions ?? {},
    }, auth.csrfToken)
    await loadPolicyScope()
  } catch (err) {
    error.value = displayError(err)
  }
}

async function saveProvider() {
  saving.value = true
  try {
    await command('/api/v2/ai/provider', 'PATCH', { ...form.value }, auth.csrfToken)
    form.value.api_key = ''
    await load()
  } catch (err) {
    error.value = displayError(err)
  } finally {
    saving.value = false
  }
}

async function testProvider() {
  testing.value = true
  try {
    await post('/api/v2/ai/provider/test', { base_url: form.value.base_url, model: form.value.model, structured_output_mode: form.value.structured_output_mode }, auth.csrfToken)
    error.value = ''
  } catch (err) {
    error.value = displayError(err)
  } finally {
    testing.value = false
  }
}

onMounted(load)
</script>

<template>
  <DashboardLayout>
    <div class="page-heading">
      <div>
        <p class="eyebrow">AI ROUTING</p>
        <h1>语义筛选</h1>
        <p>AI 负责理解内容；只有满足就绪、审批和安全条件的 ENFORCE DROP 才能抑制原始推送。</p>
      </div>
      <el-button :loading="loading" @click="load">刷新</el-button>
    </div>
    <el-alert v-if="error" type="error" :title="error" show-icon :closable="false" />
    <div class="ai-grid">
      <el-card shadow="never">
        <template #header><span>Primary Provider</span></template>
        <el-form label-position="top">
          <el-form-item label="Base URL"><el-input v-model="form.base_url" placeholder="https://example.invalid/v1" /></el-form-item>
          <el-form-item label="Model"><el-input v-model="form.model" placeholder="model-name" /></el-form-item>
          <el-form-item label="Structured output"><el-select v-model="form.structured_output_mode"><el-option label="JSON Schema" value="json_schema" /><el-option label="JSON Object" value="json_object" /></el-select></el-form-item>
          <el-form-item label="API key（只写入 Secret Store，不会回显）"><el-input v-model="form.api_key" type="password" show-password autocomplete="new-password" /></el-form-item>
          <div class="button-row"><el-button type="primary" :loading="saving" @click="saveProvider">保存配置</el-button><el-button :loading="testing" @click="testProvider">测试连接</el-button></div>
        </el-form>
        <el-tag v-if="provider?.credential_configured" type="success">credential 已配置</el-tag>
        <el-tag v-else type="info">尚未配置 credential</el-tag>
      </el-card>
      <el-card shadow="never">
        <template #header><div class="card-heading"><span>ENFORCE 就绪</span><el-tag :type="readiness?.ready && !emergencyDisabled ? 'success' : 'warning'">{{ emergencyDisabled ? '紧急禁用' : readiness?.ready ? '可用' : '锁定' }}</el-tag></div></template>
        <el-skeleton v-if="loading && !readiness" :rows="4" animated />
        <template v-else-if="readiness">
          <div class="metric-grid">
            <div><strong>{{ readiness.shadow_decisions }}</strong><span>Shadow 决策</span></div>
            <div><strong>{{ readiness.reviewed_suggested_drop }}</strong><span>已复核 DROP</span></div>
            <div><strong>{{ (readiness.drop_precision * 100).toFixed(1) }}%</strong><span>DROP precision</span></div>
            <div><strong>{{ (readiness.parse_success * 100).toFixed(1) }}%</strong><span>解析成功率</span></div>
          </div>
          <p class="muted">当前 Release：{{ readiness.current_release_id || '尚未激活' }}</p>
          <p v-if="readiness.reasons.length" class="muted">锁定原因：{{ readiness.reasons.join('、') }}</p>
          <div class="button-row">
            <el-button size="small" type="primary" :loading="phase5Busy" :disabled="!readiness.ready || emergencyDisabled" @click="approveEnforce">审批当前 Release</el-button>
            <el-button size="small" :loading="phase5Busy" @click="revokeEnforce">撤销审批</el-button>
            <el-button v-if="!emergencyDisabled" size="small" type="danger" plain :loading="phase5Busy" @click="disableEmergency">紧急禁用</el-button>
            <el-button v-else size="small" :loading="phase5Busy" @click="enableEmergency">恢复 ENFORCE</el-button>
          </div>
        </template>
      </el-card>
      <el-card shadow="never">
        <template #header><span>Media Cache</span></template>
        <div v-if="mediaSummary" class="metric-grid">
          <div><strong>{{ mediaSummary.entries }}</strong><span>文件</span></div>
          <div><strong>{{ mediaSummary.bytes }}</strong><span>字节</span></div>
          <div><strong>{{ mediaSummary.linked_events }}</strong><span>关联事件</span></div>
          <div><strong>{{ mediaSummary.expired }}</strong><span>待清理</span></div>
        </div>
        <p class="muted">仅缓存公开媒体；回放顺序为缓存 → 远端 → 文本/链接。</p>
      </el-card>
      <el-card shadow="never">
        <template #header><span>Shadow Summary</span></template>
        <el-skeleton v-if="loading && !summary" :rows="4" animated />
        <div v-else-if="summary" class="metric-grid">
          <div><strong>{{ summary.total }}</strong><span>决策</span></div>
          <div><strong>{{ summary.suggested_drop }}</strong><span>建议 DROP</span></div>
          <div><strong>{{ summary.total_tokens }}</strong><span>Tokens</span></div>
          <div><strong>{{ summary.cost_micros }}</strong><span>成本（微单位）</span></div>
        </div>
        <p class="muted">effective action 永远为 PASS。</p>
      </el-card>
    </div>
    <el-card shadow="never" class="ai-card">
      <template #header><div class="card-heading"><span>Classifier Profiles（{{ profiles.length }}）</span><el-button size="small" type="primary" @click="openProfile()">新建 Profile</el-button></div></template>
      <el-table :data="profiles" stripe>
        <el-table-column prop="name" label="Profile" />
        <el-table-column prop="id" label="ID" />
        <el-table-column prop="default_action" label="默认动作" />
        <el-table-column label="类型"><template #default="scope"><el-tag v-if="scope.row.builtin" type="info">内置</el-tag><span v-else>自定义</span></template></el-table-column>
        <el-table-column label="操作" width="250"><template #default="scope"><el-button v-if="!scope.row.builtin" size="small" @click="openProfile(scope.row)">编辑</el-button><el-button size="small" @click="cloneProfile(scope.row)">克隆</el-button><el-button v-if="!scope.row.builtin" size="small" type="danger" plain @click="deleteProfile(scope.row)">删除</el-button></template></el-table-column>
      </el-table>
    </el-card>
    <el-dialog v-model="profileDialogVisible" :title="editingProfile ? '编辑 Profile' : '新建 Profile'" width="680px">
      <el-form label-position="top">
        <el-form-item label="名称"><el-input v-model="profileForm.name" maxlength="128" /></el-form-item>
        <el-form-item label="描述"><el-input v-model="profileForm.description" type="textarea" :rows="2" maxlength="512" /></el-form-item>
        <el-form-item label="默认动作"><el-select v-model="profileForm.default_action"><el-option label="Inherit" value="inherit" /><el-option label="PASS" value="pass" /><el-option label="DROP" value="drop" /></el-select></el-form-item>
        <el-form-item label="Category actions（JSON 对象）"><el-input v-model="profileForm.category_actions" type="textarea" :rows="3" /></el-form-item>
        <el-form-item label="Tag actions（JSON 对象）"><el-input v-model="profileForm.tag_actions" type="textarea" :rows="3" /></el-form-item>
        <el-form-item label="Safety（JSON 对象）"><el-input v-model="profileForm.safety" type="textarea" :rows="2" /></el-form-item>
      </el-form>
      <template #footer><el-button @click="profileDialogVisible = false">取消</el-button><el-button type="primary" :loading="profileSaving" @click="saveProfile">保存</el-button></template>
    </el-dialog>
    <div class="ai-grid">
      <el-card shadow="never">
        <template #header><span>AI Policy（System / Global / Source / Target / Subscription）</span></template>
        <el-form label-position="top">
          <div class="case-fields">
            <el-form-item label="作用域"><el-select v-model="policyScope" @change="onPolicyScopeChange"><el-option label="Global" value="global" /><el-option label="Source" value="source" /><el-option label="Target" value="target" /><el-option label="Subscription" value="subscription" /></el-select></el-form-item>
            <el-form-item v-if="policyScope !== 'global'" label="Scope entity">
              <el-select v-model="policyScopeID" filterable clearable style="min-width: 320px" :loading="policyLoading" placeholder="选择真实的 Source / Target / Subscription" @change="loadPolicyScope">
                <el-option v-for="option in selectedPolicyScopeOptions" :key="option.id" :label="option.label" :value="option.id">
                  <div class="scope-option"><span>{{ option.label }}</span><small>{{ option.detail }}</small></div>
                </el-option>
              </el-select>
            </el-form-item>
          </div>
          <div v-if="policyScope !== 'global'" class="button-row"><span v-if="!selectedPolicyScopeOptions.length" class="muted">暂无可用实体；请先在 Sources / Targets 建立真实对象。</span><el-button v-else size="small" :loading="policyLoading" @click="loadPolicyScope">加载作用域策略</el-button></div>
          <el-form-item label="AI mode">
            <el-select v-model="policy.mode">
              <el-option label="Shadow" value="shadow" />
              <el-option label="Off" value="off" />
              <el-option label="Enforce（需审批）" value="enforce" />
              <el-option label="Inherit" value="inherit" />
            </el-select>
          </el-form-item>
          <el-form-item label="Confidence threshold"><el-input-number v-model="policy.threshold" :min="0" :max="1" :step="0.01" /></el-form-item>
          <el-form-item label="Profile">
            <el-select v-model="policy.profile_id" clearable>
              <el-option v-for="profile in profiles" :key="profile.id" :label="profile.name" :value="profile.id" />
            </el-select>
          </el-form-item>
          <el-button type="primary" @click="savePolicy">保存策略</el-button>
        </el-form>
        <p class="muted">ENFORCE 只有在当前 Release 通过全部门槛且存在匹配审批时才会生效；任何异常均 PASS。</p>
      </el-card>
      <el-card shadow="never">
        <template #header><span>Evaluation Runs（{{ runs.length }}）</span></template>
        <el-table :data="runs" stripe>
          <el-table-column prop="created_at" label="时间" width="190" />
          <el-table-column prop="status" label="状态" width="120" />
          <el-table-column prop="classifier_release_id" label="Release" />
          <el-table-column prop="total_cost_micros" label="成本（微单位）" width="130" />
        </el-table>
      </el-card>
    </div>
    <el-card shadow="never" class="ai-card">
      <template #header><span>Recent Decisions</span></template>
      <el-table :data="decisions" stripe>
        <el-table-column prop="created_at" label="时间" width="190" />
        <el-table-column prop="status" label="状态" width="150" />
        <el-table-column prop="classification.category" label="分类" width="150" />
        <el-table-column prop="suggested_action" label="建议" width="100" />
        <el-table-column prop="effective_action" label="实际动作" width="100" />
        <el-table-column prop="hard_pass_reason" label="安全原因" />
        <el-table-column label="复核" width="100"><template #default="scope"><el-tag v-if="scope.row.reviewed" type="success">已复核</el-tag><el-button v-else size="small" @click="reviewDecision(scope.row)">标记复核</el-button></template></el-table-column>
      </el-table>
    </el-card>
    <div class="ai-grid">
      <el-card shadow="never">
        <template #header><span>Recent Route Decisions（{{ routeDecisions.length }}）</span></template>
        <el-table :data="routeDecisions" stripe>
          <el-table-column prop="created_at" label="时间" width="180" />
          <el-table-column prop="configured_mode" label="配置" width="100" />
          <el-table-column prop="effective_action" label="动作" width="90" />
          <el-table-column prop="reason_code" label="原因" />
          <el-table-column label="操作" width="90"><template #default="scope"><el-button v-if="scope.row.effective_action === 'drop'" size="small" @click="replayDecision(scope.row)">回放</el-button></template></el-table-column>
        </el-table>
      </el-card>
      <el-card shadow="never">
        <template #header><span>Delivery 状态（{{ deliveries.length }}）</span></template>
        <el-table :data="deliveries" stripe>
          <el-table-column prop="created_at" label="时间" width="180" />
          <el-table-column prop="status" label="状态" width="140" />
          <el-table-column prop="target_id" label="Target" />
          <el-table-column prop="result_code" label="结果" />
        </el-table>
      </el-card>
    </div>
    <el-card shadow="never" class="ai-card">
      <template #header><div class="card-heading"><span>Evaluation Dataset（{{ cases.length }}）</span><el-button size="small" @click="exportCases">导出</el-button></div></template>
      <el-form label-position="top" class="case-form">
        <el-form-item label="NormalizedEvent JSON"><el-input v-model="caseForm.snapshot" type="textarea" :rows="3" placeholder='{"schema_version":1,"platform":"bilibili",...}' /></el-form-item>
        <div class="case-fields"><el-form-item label="Expected importance"><el-input v-model="caseForm.expected_importance" /></el-form-item><el-form-item label="Expected action"><el-select v-model="caseForm.expected_action" clearable><el-option label="PASS" value="pass" /><el-option label="DROP" value="drop" /></el-select></el-form-item><el-form-item label="Label kind"><el-select v-model="caseForm.label_kind"><el-option label="Synthetic" value="synthetic" /><el-option label="Real reviewed" value="real_reviewed" /></el-select></el-form-item></div>
        <el-button type="primary" @click="createCase">添加样本</el-button>
      </el-form>
      <el-table :data="cases" stripe><el-table-column prop="id" label="ID" /><el-table-column prop="label_kind" label="标签" width="130" /><el-table-column prop="expected_action" label="期望动作" width="120" /><el-table-column prop="source" label="来源" /><el-table-column label="操作" width="90"><template #default="scope"><el-button size="small" type="danger" plain @click="deleteCase(scope.row)">删除</el-button></template></el-table-column></el-table>
    </el-card>
  </DashboardLayout>
</template>
