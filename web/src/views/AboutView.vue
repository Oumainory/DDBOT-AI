<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { get, displayError, isUnauthorized } from '../api/client'
import type { About } from '../api/types'
import DashboardLayout from '../components/DashboardLayout.vue'
import { useAuthStore } from '../stores/auth'
import { useRouter } from 'vue-router'

const about = ref<About | null>(null)
const error = ref('')
const auth = useAuthStore()
const router = useRouter()
onMounted(async () => {
  try {
    about.value = await get<About>('/api/v2/about')
  } catch (err) {
    if (isUnauthorized(err)) {
      auth.clear()
      auth.status = 'unauthenticated'
      await router.replace({ name: 'login' })
      return
    }
    error.value = displayError(err)
  }
})
</script>

<template>
  <DashboardLayout>
    <div class="page-heading">
      <div>
        <p class="eyebrow">ABOUT</p>
        <h1>关于 DDBOT-AI</h1>
        <p>项目版本与开源许可信息。</p>
      </div>
    </div>
    <el-alert v-if="error" type="error" :title="error" show-icon :closable="false" />
    <el-card v-if="about" class="about-card" shadow="never">
      <dl class="about-list">
        <div><dt>产品</dt><dd>{{ about.product_name }}</dd></div>
        <div><dt>版本</dt><dd>{{ about.version }}</dd></div>
        <div><dt>Commit</dt><dd class="mono">{{ about.commit }}</dd></div>
        <div><dt>Build</dt><dd class="mono">{{ about.build_time }}</dd></div>
        <div><dt>许可证</dt><dd>{{ about.license_name }}</dd></div>
        <div><dt>源代码</dt><dd><a :href="about.source_repository" target="_blank" rel="noreferrer">{{ about.source_repository }}</a></dd></div>
        <div v-if="about.commit_url"><dt>Commit 链接</dt><dd><a :href="about.commit_url" target="_blank" rel="noreferrer">{{ about.commit_url }}</a></dd></div>
      </dl>
    </el-card>
  </DashboardLayout>
</template>
