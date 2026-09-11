<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { command, displayError, get, isUnauthorized, request } from '../api/client'
import type { Connector } from '../api/types'
import DashboardLayout from '../components/DashboardLayout.vue'
import { useAuthStore } from '../stores/auth'
import { useRouter } from 'vue-router'

const auth = useAuthStore(); const router = useRouter(); const connectors = ref<Connector[]>([]); const loading = ref(false); const error = ref(''); const testing = ref('')
function handleError(value: unknown) { if (isUnauthorized(value)) { auth.clear(); auth.status = 'unauthenticated'; void router.replace({ name: 'login' }); return }; error.value = displayError(value) }
async function load() { loading.value = true; error.value = ''; try { connectors.value = (await get<{ items: Connector[] }>('/api/v2/connectors')).items ?? [] } catch (value) { handleError(value) } finally { loading.value = false } }
async function test(item: Connector) { testing.value = item.id; error.value = ''; try { await request(`/api/v2/connectors/${encodeURIComponent(item.id)}/test`, { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': auth.csrfToken }, body: '{}' }) } catch (value) { handleError(value) } finally { testing.value = '' } }
async function toggle(item: Connector) { error.value = ''; try { await command(`/api/v2/connectors/${encodeURIComponent(item.id)}`, 'PATCH', { enabled: !item.enabled }, auth.csrfToken); await load() } catch (value) { handleError(value) } }
async function changeKind(item: Connector, kind: string) { if (!kind || kind === item.kind) return; error.value = ''; try { await command(`/api/v2/connectors/${encodeURIComponent(item.id)}`, 'PATCH', { kind }, auth.csrfToken); await load() } catch (value) { handleError(value); await load() } }
onMounted(load)
</script>

<template>
  <DashboardLayout>
    <div class="page-heading"><div><p class="eyebrow">CONNECTORS</p><h1>连接器</h1><p>连接器只保存拓扑和 Secret Store 引用，不会显示明文凭据。</p></div><el-button :loading="loading" @click="load">刷新</el-button></div>
    <el-alert v-if="error" type="warning" :title="error" show-icon :closable="false" />
    <el-card v-for="item in connectors" :key="item.id" class="domain-card" shadow="never"><div class="connector-row"><div><span class="card-kicker">{{ item.role.toUpperCase() }}</span><h2>{{ item.name }}</h2><p>{{ item.kind }} · {{ item.status }} · {{ item.credential_configured ? 'Secret 已配置' : '未配置 Secret' }}</p><code>{{ item.id }}</code></div><div class="connector-actions"><el-select :model-value="item.kind" size="small" aria-label="连接器类型" @change="changeKind(item, $event)"><el-option label="OneBot" value="onebot" /><el-option label="Satori" value="satori" /><el-option label="Telegram" value="telegram" /></el-select><el-tag :type="item.enabled ? 'success' : 'info'">{{ item.enabled ? '启用' : '停用' }}</el-tag><el-button @click="toggle(item)">{{ item.enabled ? '停用' : '启用' }}</el-button><el-button type="primary" :loading="testing === item.id" @click="test(item)">测试连接</el-button></div></div></el-card>
    <el-empty v-if="!loading && !connectors.length" description="尚未发现 Connector；Projection 重建后会创建 Legacy OneBot 主连接器" />
  </DashboardLayout>
</template>
