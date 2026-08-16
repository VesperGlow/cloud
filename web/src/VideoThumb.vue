<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import type { DriveFile } from './api'

// 视频缩略图：先请求持久化缩略图（服务端已存则浏览器长期缓存）；没有就
// 懒加载抽帧（进入视口附近才生成），抽到后上传持久化，之后刷新/重进目录
// 都直接命中缓存。失败回退到图标。
const props = defineProps<{ file: DriveFile }>()
const emit = defineEmits<{ (e:'failed'):void }>()
const rootEl = ref<HTMLElement|null>(null)
const thumb = ref('')

const sessionCache = new Map<string,string>()
const thumbURL = computed(() => `/api/files/${props.file.id}/thumbnail?v=${encodeURIComponent(props.file.etag || '')}`)

let disposed = false
let io: IntersectionObserver|null = null

onMounted(() => {
  const cached = sessionCache.get(cacheKey())
  if (cached) { thumb.value = cached; return }
  if (disposed) return
  fetch(thumbURL.value, { credentials: 'same-origin' })
    .then(r => {
      if (disposed) return
      if (r.ok) { thumb.value = thumbURL.value; return }
      scheduleCapture()
    })
    .catch(() => scheduleCapture())
})
onBeforeUnmount(() => {
  disposed = true
  io?.disconnect()
  io = null
})

function cacheKey(){ return props.file.id + ':' + (props.file.etag || '') }

function scheduleCapture(){
  if (disposed) return
  if (!('IntersectionObserver' in window)) { capture(); return }
  io = new IntersectionObserver(entries => {
    for (const entry of entries) {
      if (entry.isIntersecting) { io?.disconnect(); io = null; capture() }
    }
  }, { rootMargin: '240px' })
  io.observe(rootEl.value!)
}

async function capture(){
  if (disposed) return
  const url = await captureFrame()
  if (disposed) return
  if (!url) { emit('failed'); return }
  sessionCache.set(cacheKey(), url)
  thumb.value = url
  persist(url)
}

function captureFrame(): Promise<string|null> {
  return new Promise(resolve => {
    const video = document.createElement('video')
    video.muted = true
    video.playsInline = true
    video.preload = 'metadata'
    video.crossOrigin = 'anonymous'
    video.src = `/api/files/${props.file.id}/preview`
    let finished = false
    const timer = window.setTimeout(() => finish(null), 10000)
    const cleanup = () => {
      video.removeAttribute('src')
      video.load()
    }
    const draw = () => {
      if (finished) return
      try {
        const canvas = document.createElement('canvas')
        const scale = Math.min(1, 480 / Math.max(1, video.videoWidth))
        canvas.width = Math.max(1, Math.round(video.videoWidth * scale))
        canvas.height = Math.max(1, Math.round(video.videoHeight * scale))
        canvas.getContext('2d')?.drawImage(video, 0, 0, canvas.width, canvas.height)
        finish(canvas.toDataURL('image/jpeg', 0.72))
      } catch { finish(null) }
    }
    const finish = (url: string|null) => {
      if (finished) return
      finished = true
      window.clearTimeout(timer)
      cleanup()
      resolve(url)
    }
    video.addEventListener('loadeddata', () => {
      if (finished) return
      if (video.duration > 0 && video.currentTime === 0) video.currentTime = Math.min(1, video.duration * 0.1)
      else draw()
    })
    video.addEventListener('seeked', () => draw(), { once: true })
    video.addEventListener('error', () => finish(null))
    video.load()
  })
}

async function persist(dataURL: string){
  try {
    const blob = await (await fetch(dataURL)).blob()
    await fetch(`/api/files/${props.file.id}/thumbnail`, {
      method: 'PUT',
      headers: { 'Content-Type': 'image/jpeg' },
      body: blob,
      credentials: 'same-origin',
    })
  } catch { /* 持久化失败不影响本次展示，下次进入再试 */ }
}
</script>

<template>
  <div ref="rootEl" class="video-thumb">
    <img v-if="thumb" :src="thumb" alt="" loading="lazy">
    <slot v-else />
  </div>
</template>
