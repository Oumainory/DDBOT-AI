<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { displayError } from '../api/client'
import { useAuthStore } from '../stores/auth'

const auth = useAuthStore()
const router = useRouter()
const username = ref('')
const password = ref('')
const error = ref('')
const loading = ref(false)

async function submit() {
  error.value = ''
  loading.value = true
  try {
    await auth.login(username.value, password.value)
    password.value = ''
    await router.push({ name: 'overview' })
  } catch (err) {
    error.value = displayError(err)
    password.value = ''
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="auth-page">
    <el-card class="auth-card" shadow="never">
      <div class="auth-heading">
        <p class="eyebrow">DDBOT-AI</p>
        <h1>登录 Dashboard</h1>
        <p>使用管理员账户查看平台基础状态。</p>
      </div>
      <el-alert v-if="error" type="error" :title="error" show-icon :closable="false" />
      <el-form class="auth-form" @submit.prevent="submit">
        <el-form-item label="用户名">
          <el-input v-model="username" autocomplete="username" />
        </el-form-item>
        <el-form-item label="密码">
          <el-input v-model="password" type="password" show-password autocomplete="current-password" />
        </el-form-item>
        <el-button class="primary-button" type="primary" native-type="submit" :loading="loading">登录</el-button>
      </el-form>
    </el-card>
  </div>
</template>
