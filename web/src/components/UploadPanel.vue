<script setup lang="ts">
import { computed } from 'vue'
import { formatSize } from '../format'
import type { UploadTask } from '../types'

const props=defineProps<{tasks:UploadTask[]}>()
defineEmits<{clear:[];cancel:[task:UploadTask];retry:[task:UploadTask]}>()

const unfinished=computed(()=>props.tasks.filter(task=>task.status!=='done'&&task.status!=='cancelled'))
</script>

<template>
  <section v-if="tasks.length" class="upload-panel">
    <header><div><strong>上传</strong><span v-if="unfinished.length">{{ unfinished.length }} 项进行中</span></div><button @click="$emit('clear')">清除已完成</button></header>
    <div class="task-list">
      <article v-for="task in tasks" :key="task.id">
        <div class="task-top">
          <span class="task-icon">↑</span>
          <div><strong>{{ task.file.name }}</strong><small>{{ formatSize(task.file.size) }} · {{ task.status==='queued'?'等待中':task.status==='uploading'?'正在上传':task.status==='done'?'已完成':task.status==='cancelled'?'已取消':task.error }}</small></div>
          <b>{{ task.progress }}%</b>
          <button v-if="task.status==='queued'||task.status==='uploading'" @click="$emit('cancel',task)">×</button>
          <button v-else-if="task.status==='failed'" @click="$emit('retry',task)">重试</button>
        </div>
        <div class="progress"><i :class="task.status" :style="{width:`${task.progress}%`}"></i></div>
      </article>
    </div>
  </section>
</template>
