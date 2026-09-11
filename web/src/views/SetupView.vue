<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { displayError } from '../api/client'
import { useAuthStore } from '../stores/auth'

const auth = useAuthStore()
const router = useRouter()
const setupToken = ref('')
const username = ref('')
const password = ref('')
const confirmPassword = ref('')
const error = ref('')
const loading = ref(false)

async function submit() {
  error.value = ''
  if (!setupToken.value || !username.value || !password.value) {
    error.value = '请填写所有字段'
    return
  }
  if (password.value !== confirmPassword.value) {
    error.value = '两次输入的密码不一致'
    return
  }
  loading.value = true
  try {
    await auth.setup(setupToken.value, username.value, password.value)
    setupToken.value = ''
    username.value = ''
    password.value = ''
    confirmPassword.value = ''
    await router.push({ name: 'login' })
  } catch (err) {
    error.value = displayError(err)
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
        <h1>首次初始化</h1>
        <p>创建唯一的 Dashboard 管理员账户。</p>
      </div>
      <el-alert v-if="error" type="error" :title="error" show-icon :closable="false" />
      <el-form class="auth-form" @submit.prevent="submit">
        <el-form-item label="Setup Token">
          <el-input v-model="setupToken" type="password" show-password autocomplete="off" placeholder="从服务器首次启动输出中获取" />
        </el-form-item>
        <el-form-item label="用户名">
          <el-input v-model="username" autocomplete="username" />
        </el-form-item>
        <el-form-item label="密码">
          <el-input v-model="password" type="password" show-password autocomplete="new-password" />
        </el-form-item>
        <el-form-item label="确认密码">
          <el-input v-model="confirmPassword" type="password" show-password autocomplete="new-password" />
        </el-form-item>
        <el-button class="primary-button" type="primary" native-type="submit" :loading="loading">完成初始化</el-button>
      </el-form>
    </el-card>
  </div>
</template>
