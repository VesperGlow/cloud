<script setup lang="ts">
import { computed, ref } from 'vue'
import type { DriveFile } from '../api'
import { formatSize } from '../format'

const props=defineProps<{
  breadcrumbs:DriveFile[]
  current:DriveFile|null
  canGoUp:boolean
  itemCount:number
  totalBytes:number
  fileCount:number
  viewMode:'list'|'grid'
  trashMode:boolean
}>()

const emit=defineEmits<{
  openFolder:[id:string]
  up:[]
  setView:[mode:'list'|'grid']
  newDocument:[]
  createFolder:[]
  upload:[]
  leaveTrash:[]
  emptyTrash:[]
}>()

const createMenu=ref<HTMLDetailsElement|null>(null)
const parentBreadcrumbs=computed(()=>props.breadcrumbs.slice(0,-1))

function runCreate(action:'document'|'folder'){
  createMenu.value?.removeAttribute('open')
  if(action==='document')emit('newDocument')
  else emit('createFolder')
}
</script>

<template>
  <div class="content-head">
    <div class="folder-heading">
      <nav v-if="!trashMode&&parentBreadcrumbs.length" class="breadcrumbs" aria-label="路径">
        <button v-for="crumb in parentBreadcrumbs" :key="crumb.id" @click="$emit('openFolder',crumb.id)">
          {{ crumb.name || '我的文件' }}<span>/</span>
        </button>
      </nav>
      <div class="title-row">
        <button v-if="!trashMode&&canGoUp" class="up-button" title="返回上一级" aria-label="返回上一级" @click="$emit('up')">
          <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 19V6m0 0-5 5m5-5 5 5"/></svg>
        </button>
        <h1>{{ trashMode?'回收站':current?.name || '我的文件' }}</h1>
        <div v-if="!trashMode" class="view-switch" role="group" aria-label="文件显示方式">
          <button :class="{active:viewMode==='list'}" title="列表视图" aria-label="列表视图" @click="$emit('setView','list')">
            <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M8 6h12M8 12h12M8 18h12M4 6h.01M4 12h.01M4 18h.01"/></svg>
          </button>
          <button :class="{active:viewMode==='grid'}" title="大图标视图" aria-label="大图标视图" @click="$emit('setView','grid')">
            <svg viewBox="0 0 24 24" aria-hidden="true"><rect x="4" y="4" width="6" height="6" rx="1"/><rect x="14" y="4" width="6" height="6" rx="1"/><rect x="4" y="14" width="6" height="6" rx="1"/><rect x="14" y="14" width="6" height="6" rx="1"/></svg>
          </button>
        </div>
      </div>
      <p class="folder-meta">
        <span>{{ itemCount }} 个项目</span><i></i><template v-if="trashMode"><span>永久删除前可恢复</span></template><template v-else>
          <span>共 {{ fileCount }} 个文件</span><i></i>
          <span>{{ formatSize(totalBytes) }}</span>
        </template>
      </p>
    </div>
    <div v-if="trashMode" class="actions"><button class="secondary" @click="$emit('leaveTrash')">返回我的文件</button><button class="trash-empty-action" :disabled="!itemCount" @click="$emit('emptyTrash')">清空回收站</button></div>
    <div v-else class="actions">
      <div class="desktop-create-actions">
        <button class="secondary" @click="$emit('newDocument')">＋ 新建文档</button>
        <button class="secondary" @click="$emit('createFolder')">＋ 新建文件夹</button>
      </div>
      <details ref="createMenu" class="create-menu">
        <summary class="secondary">＋ 新建</summary>
        <div class="create-menu-popover">
          <button @click="runCreate('document')"><span>▤</span><div><b>新建文档</b><small>Markdown 或纯文本</small></div></button>
          <button @click="runCreate('folder')"><span>▰</span><div><b>新建文件夹</b><small>整理当前目录</small></div></button>
        </div>
      </details>
      <button class="primary upload-action" @click="$emit('upload')">↑ 上传文件</button>
    </div>
  </div>
</template>

<style scoped>
.trash-empty-action{min-height:40px;padding:0 16px;border:1px solid #fecaca;border-radius:10px;background:#fff5f5;color:#dc2626;font-weight:750}.trash-empty-action:hover:not(:disabled){background:#fee2e2}.trash-empty-action:disabled{opacity:.45}
</style>
