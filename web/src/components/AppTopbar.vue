<script setup lang="ts">
defineProps<{
  user:string
  hasAvatar:boolean
  avatarUrl:string
}>()

defineEmits<{
  home:[]
  account:[]
  logout:[]
  avatarError:[]
}>()
</script>

<template>
  <header class="topbar">
    <button class="logo brand-button" title="回到我的文件" @click="$emit('home')">
      <span class="brand-mark small"><img src="/logo.png" alt=""></span>
      <span>Cloud</span>
    </button>
    <div class="top-actions">
      <span class="connection"><i></i>S3 块直传</span>
      <button class="account-button" title="打开账户设置" @click="$emit('account')">
        <span class="avatar-badge">
          <img v-if="hasAvatar" :src="avatarUrl" alt="个人头像" @error="$emit('avatarError')">
          <template v-else>{{ user.slice(0,1).toUpperCase() }}</template>
        </span>
        <span class="account-copy"><b>{{ user }}</b><small>账户设置</small></span>
      </button>
      <button class="top-logout" @click="$emit('logout')">退出</button>
    </div>
  </header>
</template>
