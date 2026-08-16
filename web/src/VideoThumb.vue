<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import type { DriveFile } from './api'

// 视频缩略图：懒加载（进入视口附近才抽帧），把视频开头附近的一帧画到
// canvas 存成 JPEG；按内容哈希（etag）做会话级缓存，失败回退到图标。
const props = defineProps<{ file: DriveFile }>()
const emit = defineEmits<{ (e:'failed'):void }>()
const rootEl = ref<HTMLElement|null>(null)
const thumb = ref('')

const sessionCache = new Map<string,string>()

function cacheKey(){ return props.file.id + ':' + (props.file.etag || '') }

let disposed = false
let io: IntersectionObserver|null = null

onMounted(() => {
  const cached = sessionCache.get(cacheKey())
  if (cached) { thumb.value = cached; return }
  if (disposed) return
  if (!('IntersectionObserver' in window)) { capture(); return }
  io = new IntersectionObserver(entries => {
    for (const entry of entries) {
      if (entry.isIntersecting) { io?.disconnect(); io = null; capture(); }
    }
  }, { rootMargin: '240px' })
  io.observe(rootEl.value!)
})
onBeforeUnmount(() => {
  disposed = true
  io?.disconnect()
  io = null
})

function capture(){
  if (disposed) return
  const video = document.createElement('video')
  video.muted = true
  video.playsInline = true
  video.preload = 'metadata'
  video.crossOrigin = 'anonymous'
  video.src = `/api/files/${props.file.id}/preview`
  let finished = false
  const timer = window.setTimeout(() => fail(), 10000)
  const cleanup = () => {
    video.removeAttribute('src')
    video.load()
  }
  const fail = () => {
    if (finished) return
    finished = true
    window.clearTimeout(timer)
    cleanup()
    emit('failed')
  }
  const done = () => {
    if (finished) return
    finished = true
    window.clearTimeout(timer)
    try {
      const canvas = document.createElement('canvas')
      const scale = Math.min(1, 480 / Math.max(1, video.videoWidth))
      canvas.width = Math.max(1, Math.round(video.videoWidth * scale))
      canvas.height = Math.max(1, Math.round(video.videoHeight * scale))
      canvas.getContext('2d')?.drawImage(video, 0, 0, canvas.width, canvas.height)
      const url = canvas.toDataURL('image/jpeg', 0.72)
      sessionCache.set(cacheKey(), url)
      if (!disposed) thumb.value = url
      cleanup()
    } catch { fail() }
  }
  video.addEventListener('loadeddata', () => {
    if (finished) return
    if (video.duration > 0 && video.currentTime === 0) {
      video.currentTime = Math.min(1, video.duration * 0.1)
    } else {
      done()
    }
  })
  video.addEventListener('seeked', () => done(), { once: true })
  video.addEventListener('error', fail)
  video.load()
}
</script>

<template>
  <div ref="rootEl" class="video-thumb">
    <img v-if="thumb" :src="thumb" alt="" loading="lazy">
    <slot v-else />
  </div>
</template>
