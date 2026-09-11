<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { command, displayError, get, isUnauthorized, request } from '../api/client'
import type { Connector, Target } from '../api/types'
import DashboardLayout from '../components/DashboardLayout.vue'
import { useAuthStore } from '../stores/auth'
import { useRouter } from 'vue-router'

type Migration = { migration_id: string; old_connector_id: string; new_connector_id: string; state: string; progress_marker: string; affected_target_ids_json?: string }
type Mapping = { old_target_id: string; old_connector_id: string; old_target_type: string; old_external_id: string; new_target_id?: string; new_connector_id?: string; new_target_type?: string; new_external_id?: string; mapping_status: string }

const auth = useAuthStore()
const router = useRouter()
const connectors = ref<Connector[]>([])
const loading = ref(false)
const error = ref('')
const testing = ref('')
const wizardOpen = ref(false)
const wizardStep = ref(0)
const wizardBusy = ref(false)
const wizardNotice = ref('')
const selectedConnector = ref<Connector | null>(null)
const destinationID = ref('')
const migration = ref<Migration | null>(null)
const mappings = ref<Mapping[]>([])
const migrationTargets = ref<Target[]>([])
const pairingCode = ref('')
const pairingChallenge = ref<{ challenge_id: string; expires_at: number } | null>(null)
const pairingOpen = ref(false)

function handleError(value: unknown) {
  if (isUnauthorized(value)) {
    auth.clear()
    auth.status = 'unauthenticated'
    void router.replace({ name: 'login' })
    return
  }
  error.value = displayError(value)
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    connectors.value = (await get<{ items: Connector[] }>('/api/v2/connectors')).items ?? []
  } catch (value) {
    handleError(value)
  } finally {
    loading.value = false
  }
}

async function test(item: Connector) {
  testing.value = item.id
  error.value = ''
  try {
    await request(`/api/v2/connectors/${encodeURIComponent(item.id)}/test`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': auth.csrfToken },
      body: '{}',
    })
  } catch (value) {
    handleError(value)
  } finally {
    testing.value = ''
  }
}

async function toggle(item: Connector) {
  error.value = ''
  try {
    await command(`/api/v2/connectors/${encodeURIComponent(item.id)}`, 'PATCH', { enabled: !item.enabled }, auth.csrfToken)
    await load()
  } catch (value) {
    handleError(value)
  }
}

function openWizard(item: Connector) {
  selectedConnector.value = item
  destinationID.value = connectors.value.find((candidate) => candidate.id !== item.id && candidate.role === 'main' && candidate.kind !== item.kind)?.id ?? ''
  wizardStep.value = 0
  wizardNotice.value = ''
  migration.value = null
  mappings.value = []
  migrationTargets.value = []
  wizardOpen.value = true
}

async function createMigration() {
  if (!selectedConnector.value || !destinationID.value) {
    wizardNotice.value = '请选择目标 Main Connector'
    return
  }
  wizardBusy.value = true
  wizardNotice.value = ''
  try {
    const result = await command<{ migration_id: string }>('/api/v2/connector-migrations', 'POST', { old_connector_id: selectedConnector.value.id, new_connector_id: destinationID.value }, auth.csrfToken)
    migration.value = result as unknown as Migration
    wizardStep.value = 1
  } catch (value) {
    wizardNotice.value = displayError(value)
  } finally {
    wizardBusy.value = false
  }
}

async function discover() {
  if (!migration.value) return
  wizardBusy.value = true
  wizardNotice.value = ''
  try {
    const result = await command(`/api/v2/connector-migrations/${migration.value.migration_id}/discover`, 'POST', {}, auth.csrfToken) as unknown as { mappings?: Mapping[] }
    mappings.value = result.mappings ?? []
    migrationTargets.value = (await get<{ items: Target[] }>('/api/v2/targets')).items?.filter((target) => target.connector_id === migration.value?.new_connector_id && target.status === 'resolved') ?? []
    wizardStep.value = 2
  } catch (value) {
    wizardNotice.value = displayError(value)
  } finally {
    wizardBusy.value = false
  }
}

async function saveMappings() {
  if (!migration.value) return
  if (mappings.value.some((mapping) => mapping.mapping_status !== 'confirmed' || !mapping.new_target_id)) {
    wizardNotice.value = '所有受影响 Target 都必须明确映射'
    return
  }
  wizardBusy.value = true
  wizardNotice.value = ''
  try {
    await command(`/api/v2/connector-migrations/${migration.value.migration_id}/mappings`, 'PATCH', { mappings: mappings.value }, auth.csrfToken)
    wizardStep.value = 3
  } catch (value) {
    wizardNotice.value = displayError(value)
  } finally {
    wizardBusy.value = false
  }
}

async function preflight() {
  if (!migration.value) return
  wizardBusy.value = true
  wizardNotice.value = ''
  try {
    migration.value = await command(`/api/v2/connector-migrations/${migration.value.migration_id}/preflight`, 'POST', {}, auth.csrfToken) as unknown as Migration
    wizardStep.value = 3
  } catch (value) {
    wizardNotice.value = displayError(value)
  } finally {
    wizardBusy.value = false
  }
}

async function commitMigration() {
  if (!migration.value) return
  wizardBusy.value = true
  try {
    migration.value = await command(`/api/v2/connector-migrations/${migration.value.migration_id}/commit`, 'POST', {}, auth.csrfToken) as unknown as Migration
    wizardStep.value = 4
    await load()
  } catch (value) {
    wizardNotice.value = displayError(value)
  } finally {
    wizardBusy.value = false
  }
}

async function pairTelegram(item: Connector) {
  try {
    const result = await command(`/api/v2/connectors/${encodeURIComponent(item.id)}/pairing`, 'POST', {}, auth.csrfToken) as unknown as { challenge: { challenge_id: string; expires_at: number }; code: string }
    pairingChallenge.value = result.challenge
    pairingCode.value = result.code
    pairingOpen.value = true
  } catch (value) {
    handleError(value)
  }
}

onMounted(load)
</script>

<template>
  <DashboardLayout>
    <div class="page-heading">
      <div>
        <p class="eyebrow">CONNECTORS</p>
        <h1>连接器</h1>
        <p>连接器只保存拓扑和 Secret Store 引用，不会显示明文凭据。</p>
      </div>
      <el-button :loading="loading" @click="load">刷新</el-button>
    </div>

    <el-alert v-if="error" type="warning" :title="error" show-icon :closable="false" />

    <el-card v-for="item in connectors" :key="item.id" class="domain-card" shadow="never">
      <div class="connector-row">
        <div>
          <span class="card-kicker">{{ item.role.toUpperCase() }}</span>
          <h2>{{ item.name }}</h2>
          <p>{{ item.kind }} · {{ item.status }} · {{ item.credential_configured ? 'Secret 已配置' : '未配置 Secret' }}</p>
          <code>{{ item.id }}</code>
        </div>
        <div class="connector-actions">
          <el-tag :type="item.enabled ? 'success' : 'info'">{{ item.enabled ? '启用' : '停用' }}</el-tag>
          <el-button @click="toggle(item)">{{ item.enabled ? '停用' : '启用' }}</el-button>
          <el-button type="primary" :loading="testing === item.id" @click="test(item)">测试连接</el-button>
          <el-button v-if="item.role === 'main'" @click="openWizard(item)">迁移 Main</el-button>
          <el-button v-if="item.kind === 'telegram'" @click="pairTelegram(item)">Pair Target</el-button>
        </div>
      </div>
    </el-card>

    <el-empty v-if="!loading && !connectors.length" description="尚未发现 Connector；Projection 重建后会创建 Legacy OneBot 主连接器" />

    <el-dialog v-model="wizardOpen" title="Connector Migration Wizard" width="min(760px, 94vw)">
      <el-steps :active="wizardStep" finish-status="success" simple>
        <el-step title="目标" />
        <el-step title="发现" />
        <el-step title="映射" />
        <el-step title="确认" />
        <el-step title="完成" />
      </el-steps>
      <el-alert v-if="wizardNotice" class="wizard-notice" type="warning" :title="wizardNotice" :closable="false" />

      <div v-if="wizardStep === 0" class="wizard-panel">
        <p>旧 Connector：{{ selectedConnector?.name }}（{{ selectedConnector?.kind }}）</p>
        <el-select v-model="destinationID" placeholder="选择新的 Main Connector" style="width: 100%">
          <el-option
            v-for="item in connectors.filter((candidate) => candidate.role === 'main' && candidate.id !== selectedConnector?.id)"
            :key="item.id"
            :label="`${item.name} (${item.kind})`"
            :value="item.id"
          />
        </el-select>
        <el-button type="primary" :loading="wizardBusy" @click="createMigration">创建迁移草稿</el-button>
      </div>

      <div v-else-if="wizardStep === 1" class="wizard-panel">
        <p>迁移草稿已持久化。发现步骤会枚举当前 Legacy projection 中的受影响 Target，仍不会切换路由。</p>
        <el-button type="primary" :loading="wizardBusy" @click="discover">发现受影响 Target</el-button>
      </div>

      <div v-else-if="wizardStep === 2" class="wizard-panel">
        <p>必须为每个旧 Target 选择明确的新 Target；系统不会猜测 Guild/Channel。</p>
        <el-table :data="mappings" size="small">
          <el-table-column prop="old_external_id" label="旧 Target" />
          <el-table-column label="新 Target">
            <template #default="scope">
              <el-select v-model="scope.row.new_target_id" placeholder="选择已验证 Target" @change="scope.row.mapping_status = scope.row.new_target_id ? 'confirmed' : 'required'">
                <el-option
                  v-for="target in migrationTargets"
                  :key="target.id"
                  :label="`${target.display_name || target.external_id} (${target.target_type})`"
                  :value="target.id"
                />
              </el-select>
            </template>
          </el-table-column>
          <el-table-column prop="mapping_status" label="状态" />
        </el-table>
        <el-button type="primary" :loading="wizardBusy" :disabled="!mappings.length" @click="saveMappings">保存映射</el-button>
      </div>

      <div v-else-if="wizardStep === 3" class="wizard-panel">
        <p>映射已保存。先运行 Preflight，确认连接器、拓扑和持久化边界，再提交切换。</p>
        <el-button type="primary" :loading="wizardBusy" @click="preflight">运行 Preflight</el-button>
        <el-button type="danger" :loading="wizardBusy" :disabled="migration?.state !== 'preflight_ready'" @click="commitMigration">确认 Commit</el-button>
      </div>

      <div v-else class="wizard-panel">
        <el-result icon="success" title="迁移完成" :sub-title="migration?.migration_id" />
        <p>Held delivery 只会按本次 migration 释放；unknown 永不自动重发。</p>
      </div>
    </el-dialog>

    <el-dialog v-if="pairingChallenge" v-model="pairingOpen" title="Telegram Pairing Code" width="min(480px, 92vw)">
      <p>请在目标 Telegram chat 中发送：</p>
      <code>/bind {{ pairingCode }}</code>
      <p>代码只显示在当前浏览器状态，不会写入 localStorage。Challenge ID：{{ pairingChallenge.challenge_id }}</p>
    </el-dialog>
  </DashboardLayout>
</template>
