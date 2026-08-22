<script setup lang="ts">
import { reactive } from 'vue'
import type { DriveFile } from '../api'
import { isAudio, isBook, isEditable, isEpub, isImage, isMedia, isVideo, previewURL, thumbSRC } from '../fileTypes'
import { formatDate, formatSize } from '../format'
import VideoThumb from '../VideoThumb.vue'

defineProps<{items:DriveFile[];selectedIds:Set<string>;trashMode?:boolean}>()
defineEmits<{open:[item:DriveFile];select:[item:DriveFile];restore:[item:DriveFile];purge:[item:DriveFile]}>()

const thumbFallbackTried=reactive<Record<string,boolean>>({})
const coverBroken=reactive<Record<string,boolean>>({})

function thumbFallback(event:Event,item:DriveFile){
  const image=event.target as HTMLImageElement
  if(thumbFallbackTried[item.id]){image.hidden=true;return}
  thumbFallbackTried[item.id]=true
  image.src=previewURL(item)
}
</script>

<template>
  <div class="file-grid">
    <article v-for="item in items" :key="item.id" class="file-card" :class="{mutedrow:item.status!=='ready',selected:selectedIds.has(item.id)}" @dblclick="(!trashMode||item.kind==='file')&&$emit('open',item)">
      <button class="card-select" :class="{active:selectedIds.has(item.id)}" :title="selectedIds.has(item.id)?'取消选择':'选择项目'" :aria-label="selectedIds.has(item.id)?'取消选择':'选择项目'" :aria-pressed="selectedIds.has(item.id)" @click.stop="$emit('select',item)">
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="m5 12 4 4L19 6"/></svg>
      </button>
      <button class="card-preview" :disabled="trashMode&&item.kind==='directory'" :title="trashMode&&item.kind==='directory'?'恢复后可打开文件夹':isBook(item)?'阅读':trashMode&&isEditable(item)?'只读查看':item.kind==='directory'?'打开文件夹':isEditable(item)?'编辑文档':isImage(item)?'预览图片':isVideo(item)?'播放视频':isAudio(item)?'播放音频':'文件'" @click="(!trashMode||item.kind==='file')&&$emit('open',item)">
        <img v-if="isImage(item)" class="ui-image" :src="thumbSRC(item)" :alt="item.name" loading="lazy" draggable="false" @error="thumbFallback($event,item)">
        <VideoThumb v-else-if="isVideo(item)" :file="item"><span class="large-video">▶</span></VideoThumb>
        <img v-else-if="isEpub(item)&&!coverBroken[item.id]" class="ui-image" :src="thumbSRC(item)" :alt="item.name" loading="lazy" draggable="false" @error="coverBroken[item.id]=true">
        <span v-else-if="isEpub(item)" class="large-document">▤</span>
        <span v-else-if="item.kind==='directory'" class="large-folder">▰</span>
        <span v-else-if="isEditable(item)" class="large-document">▤</span>
        <span v-else-if="isAudio(item)" class="large-audio">♫</span>
        <span v-else class="large-file">◇</span>
      </button>
      <div class="card-info"><strong :title="item.name">{{ item.name }}</strong><small v-if="trashMode">{{ item.kind==='directory'?'文件夹':formatSize(item.size) }} · 删除于 {{ formatDate(item.deleted_at||item.updated_at) }}</small><small v-else>{{ item.kind==='directory'?'文件夹':formatSize(item.size) }} · {{ formatDate(item.updated_at) }}</small></div>
      <div v-if="trashMode" class="card-actions"><button v-if="isBook(item)||isEditable(item)||isMedia(item)" :title="isBook(item)?'阅读':isImage(item)?'预览':isMedia(item)?'播放':'只读查看'" @click="$emit('open',item)">查看</button><button title="恢复" class="restore-action" @click="$emit('restore',item)">恢复</button><button title="永久删除" class="danger" @click="$emit('purge',item)">删除</button></div>
    </article>
  </div>
</template>
