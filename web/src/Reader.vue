<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, nextTick, reactive, ref } from 'vue'
import { api } from './api'
import type { DriveFile } from './api'

// 阅读器：整本书放进一个 CSS 多栏容器（每栏=一屏），靠 translateX 切页，
// 浏览器负责按行填满与重排；逻辑移植自 VesperGlow/reader 的 reader.js。
const props = defineProps<{ file: DriveFile }>()
const emit = defineEmits<{ (e:'close'):void }>()

interface TocEntry { label:string; path?:string; fragment?:string; offset?:number; depth?:number; index:number }

const MAX_COLUMN = 720
const PROGRESS_SAVE_DELAY = 1200

const rootEl = ref<HTMLElement|null>(null)
const viewportEl = ref<HTMLElement|null>(null)
const pageEl = ref<HTMLElement|null>(null)
const loading = ref(true)
const error = ref('')
const format = ref('')
const title = ref(props.file.name)
const toc = ref<TocEntry[]>([])
const tocPages = ref<number[]>([])
const currentPage = ref(0)
const pageCount = ref(1)
const dark = ref(false)
const fontSize = ref(19)
const tocOpen = ref(false)
const toolsHidden = ref(false)
const fontPopover = ref(false)
let contentNode: HTMLDivElement|null = null
let pageStep = 0
let pageWidth = 0
let pageHeight = 0
let restoreRatio = 0
let suppressZoneClick = 0
let resizeTimer = 0
let progressTimer = 0
let progressChain:Promise<unknown> = Promise.resolve()

const pageLabel = computed(() => {
  const percentage = pageCount.value <= 1 ? 100 : Math.round((currentPage.value / (pageCount.value - 1)) * 100)
  return `${currentPage.value + 1} / ${pageCount.value} · ${percentage}%`
})
const tocActive = computed(() => {
  let active = 0
  tocPages.value.forEach((page, index) => { if (page <= currentPage.value) active = index })
  return active
})

onMounted(async () => {
  const savedTheme = localStorage.getItem('reader-theme')
  dark.value = savedTheme === 'dark'
  const savedSize = Number(localStorage.getItem('reader-font-size'))
  if (Number.isFinite(savedSize) && savedSize > 0) fontSize.value = Math.max(14, Math.min(32, savedSize))
  applyPrefs()
  document.body.classList.add('reader-open')
  window.addEventListener('keydown', onKeydown)
  window.addEventListener('resize', scheduleRelayout)
  window.visualViewport?.addEventListener('resize', scheduleRelayout)
  document.addEventListener('visibilitychange', flushOnHidden)
  window.addEventListener('pagehide', flushProgress)
  await open()
})
onBeforeUnmount(() => {
  flushProgress()
  document.body.classList.remove('reader-open')
  window.removeEventListener('keydown', onKeydown)
  window.removeEventListener('resize', scheduleRelayout)
  window.visualViewport?.removeEventListener('resize', scheduleRelayout)
  document.removeEventListener('visibilitychange', flushOnHidden)
  window.removeEventListener('pagehide', flushProgress)
  document.title = 'Cloud · 私人网盘'
})

function applyPrefs(){
  rootEl.value?.style.setProperty('--reader-font-size', `${fontSize.value}px`)
}

async function open(){
  loading.value = true
  error.value = ''
  try{
    const info = await api<{format:string;title:string;cover:boolean;toc:TocEntry[]}>(`/api/files/${props.file.id}/book`)
    format.value = info.format
    title.value = info.title || props.file.name
    const progressP = api<{page:number;total_pages:number|null}>(`/api/files/${props.file.id}/book/progress`).catch(() => ({ page:0, total_pages:null as number|null }))
    const model = await api<{kind:string;html?:string;text?:string;toc:TocEntry[]}>(`/api/files/${props.file.id}/book/content`)
    contentNode = document.createElement('div')
    contentNode.className = `book-content ${model.kind}`
    if (model.kind === 'txt') {
      const text = model.text || ''
      const marks = (model.toc || [])
        .map((entry, index) => ({ entry, index }))
        .filter(m => typeof m.entry.offset === 'number')
        .sort((a, b) => (a.entry.offset || 0) - (b.entry.offset || 0))
      let pos = 0
      for (const { entry, index } of marks) {
        const at = Math.max(pos, Math.min(text.length, entry.offset || 0))
        if (at > pos) contentNode.appendChild(document.createTextNode(text.slice(pos, at)))
        const anchor = document.createElement('span')
        anchor.className = 'toc-anchor'
        anchor.dataset.toc = String(index)
        contentNode.appendChild(anchor)
        pos = at
      }
      contentNode.appendChild(document.createTextNode(text.slice(pos)))
    } else {
      contentNode.innerHTML = model.html || ''
    }
    toc.value = (model.toc || []).map((entry, index) => ({ ...entry, index }))
    const progress = await progressP
    if (progress.total_pages && progress.total_pages > 1) {
      restoreRatio = Math.min(1, Math.max(0, progress.page / (progress.total_pages - 1)))
    }
    await nextTick()
    activate()
    loading.value = false
    document.title = `${props.file.name} · Cloud`
  }catch(e){
    error.value = (e as Error).message
    loading.value = false
  }
}

// ---- 分栏布局 / 翻页 ----
function activate(){
  if (!contentNode) return
  if (pageEl.value!.firstElementChild !== contentNode) pageEl.value!.replaceChildren(contentNode)
  measure()
  if (restoreRatio > 0) {
    currentPage.value = Math.round(restoreRatio * Math.max(0, pageCount.value - 1))
    restoreRatio = 0
  }
  goToPage(currentPage.value)
}

function measure(){
  if (!contentNode || !viewportEl.value) return
  const node = contentNode
  const width = viewportEl.value.clientWidth
  const height = viewportEl.value.clientHeight
  let sidePad = Math.round(Math.min(Math.max(width * 0.055, 16), 44))
  if (width - 2 * sidePad > MAX_COLUMN) sidePad = Math.round((width - MAX_COLUMN) / 2)
  node.style.boxSizing = 'border-box'
  node.style.width = `${width}px`
  node.style.height = `${height}px`
  // 悬浮式工具栏：栏高固定为整屏，顶部留出顶栏高度，底部由悬浮的底栏覆盖；
  // 显示/隐藏工具只是叠层变化，视口与分栏永远不变 → 文字绝不挪动。
  node.style.padding = `64px ${sidePad}px 24px`
  node.style.columnWidth = `${Math.max(1, width - 2 * sidePad)}px`
  node.style.columnGap = `${2 * sidePad}px`
  node.style.columnFill = 'auto'
  node.style.transition = 'none'
  node.style.transform = 'translateX(0px)'
  pageWidth = width
  pageHeight = height
  pageStep = width
  pageCount.value = Math.max(1, Math.round(node.scrollWidth / width))
  mapTocPages()
}

function setTransform(x:number, animate:boolean){
  if (!contentNode) return
  const node = contentNode
  if (animate) void node.offsetWidth
  node.style.transition = animate ? 'transform .26s cubic-bezier(.22,.72,.26,1)' : 'none'
  node.style.transform = `translateX(${x}px)`
}

function goToPage(index:number, animate = false){
  currentPage.value = Math.min(Math.max(0, index), Math.max(0, pageCount.value - 1))
  setTransform(-currentPage.value * pageStep, animate)
}

function turnPage(direction:'prev'|'next'){
  const target = currentPage.value + (direction === 'next' ? 1 : -1)
  if (target < 0 || target >= pageCount.value) return
  goToPage(target, true)
  queueProgressSave()
}
function previous(){ turnPage('prev') }
function next(){ turnPage('next') }

function seekTo(page:number){
  goToPage(page)
  queueProgressSave()
}

// ---- 目录 ----
function mapTocPages(){
  if (!contentNode) return
  const baseLeft = contentNode.getBoundingClientRect().left
  tocPages.value = toc.value.map((entry, index) => {
    const target = findTocTarget(entry, index)
    let page = 0
    if (target) page = Math.max(0, Math.round((target.getBoundingClientRect().left - baseLeft) / pageStep))
    return Math.min(page, pageCount.value - 1)
  })
}

function findTocTarget(entry:TocEntry, index:number):Element|null{
  if (!contentNode) return null
  if (format.value === 'txt') return contentNode.querySelector(`[data-toc="${index}"]`)
  const candidates = Array.from(contentNode.querySelectorAll('[id], [data-frag-ids]'))
  const fragment = entry.fragment
  if (fragment) {
    const byId = candidates.find(el => el.id === fragment || (el.getAttribute('data-frag-ids') || '').split(' ').includes(fragment))
    if (byId) return byId
  }
  return Array.from(contentNode.querySelectorAll('[data-source-path]')).find(el => el.getAttribute('data-source-path') === entry.path) || null
}

function tocTargetPage(entry:TocEntry, index:number){
  const target = findTocTarget(entry, index)
  if (!target || !contentNode) return Math.max(0, tocPages.value[index] || 0)
  const baseLeft = contentNode.getBoundingClientRect().left
  const page = Math.round((target.getBoundingClientRect().left - baseLeft) / pageStep)
  return Math.min(Math.max(0, page), Math.max(0, pageCount.value - 1))
}

function jumpToc(index:number){
  goToPage(tocTargetPage(toc.value[index], index))
  queueProgressSave()
  tocOpen.value = false
}
function toggleToc(){ tocOpen.value = !tocOpen.value; if (tocOpen.value) fontPopover.value = false }

// ---- 工具 / 偏好 ----
function toggleTools(){ toolsHidden.value = !toolsHidden.value; if (toolsHidden.value) fontPopover.value = false }
function toggleTheme(){
  dark.value = !dark.value
  try { localStorage.setItem('reader-theme', dark.value ? 'dark' : 'light') } catch { /* ignore */ }
}
function stepFont(delta:number){ setFontSize(fontSize.value + delta) }
function setFontSize(size:number){
  fontSize.value = Math.max(14, Math.min(32, size))
  applyPrefs()
  try { localStorage.setItem('reader-font-size', String(fontSize.value)) } catch { /* ignore */ }
  if (contentNode) {
    // 改字号必须瞬时重排，不能用动画过渡
    relayout()
  }
}

// ---- 重排：抓住当前页顶部文字，重排后对回原位 ----
function scheduleRelayout(){
  if (!contentNode || loading.value) return
  if (viewportEl.value && viewportEl.value.clientWidth === pageWidth && viewportEl.value.clientHeight === pageHeight) return
  clearTimeout(resizeTimer)
  resizeTimer = window.setTimeout(relayout, 200)
}
function relayout(){
  if (!contentNode) return
  const anchor = captureTopAnchor()
  const fallbackRatio = pageCount.value > 1 ? currentPage.value / (pageCount.value - 1) : 0
  measure()
  let page = anchor ? anchorPage(anchor) : null
  if (page == null) page = Math.round(fallbackRatio * Math.max(0, pageCount.value - 1))
  goToPage(page)
  queueProgressSave()
}
function captureTopAnchor(){
  if (!contentNode || !viewportEl.value) return null
  const style = getComputedStyle(contentNode)
  const padLeft = parseFloat(style.paddingLeft) || 0
  const padTop = parseFloat(style.paddingTop) || 0
  const rect = viewportEl.value.getBoundingClientRect()
  return caretAt(rect.left + padLeft + 8, rect.top + padTop + 8)
}
function caretAt(x:number, y:number){
  const zones = Array.from(viewportEl.value?.querySelectorAll('.reader-zone') || []) as HTMLElement[]
  const saved = zones.map(z => z.style.pointerEvents)
  zones.forEach(z => { z.style.pointerEvents = 'none' })
  try{
    if (document.caretPositionFromPoint) {
      const pos = document.caretPositionFromPoint(x, y)
      return pos ? { node: pos.offsetNode, offset: pos.offset } : null
    }
    if (document.caretRangeFromPoint) {
      const range = document.caretRangeFromPoint(x, y)
      return range ? { node: range.startContainer, offset: range.startOffset } : null
    }
    return null
  }catch{ return null }finally{ zones.forEach((z, i) => { z.style.pointerEvents = saved[i] }) }
}
function anchorPage(anchor:{node:Node;offset:number}|null){
  if (!anchor || !contentNode || !anchor.node.isConnected || !contentNode.contains(anchor.node)) return null
  try{
    const range = document.createRange()
    const max = anchor.node.nodeType === Node.TEXT_NODE ? anchor.node.textContent!.length : anchor.node.childNodes.length
    range.setStart(anchor.node, Math.min(anchor.offset, max))
    range.collapse(true)
    const rects = range.getClientRects()
    const rect = rects.length ? rects[0] : range.getBoundingClientRect()
    if (!rect) return null
    const baseLeft = contentNode.getBoundingClientRect().left
    const page = Math.round((rect.left - baseLeft) / pageStep)
    return Math.max(0, Math.min(pageCount.value - 1, page))
  }catch{ return null }
}

// ---- 点击热区 / 滑动翻页 ----
function guardZone(handler:()=>void){
  return () => { if (Date.now() < suppressZoneClick) return; handler() }
}
const swipe = reactive({ active:false, pointerId:0, startX:0, startY:0, startTime:0, dragging:false })
function onPointerDown(e:PointerEvent){
  if (loading.value || error.value || tocOpen.value) return
  if (e.pointerType === 'mouse' && e.button !== 0) return
  if (swipe.active) return
  swipe.active = true; swipe.pointerId = e.pointerId
  swipe.startX = e.clientX; swipe.startY = e.clientY; swipe.startTime = performance.now(); swipe.dragging = false
  viewportEl.value?.setPointerCapture(e.pointerId)
}
function onPointerMove(e:PointerEvent){
  if (!swipe.active || e.pointerId !== swipe.pointerId) return
  const dx = e.clientX - swipe.startX, dy = e.clientY - swipe.startY
  if (!swipe.dragging) {
    if (Math.abs(dx) < 8) return
    if (Math.abs(dx) < Math.abs(dy) * 1.2) { swipe.active = false; return }
    swipe.dragging = true
    suppressZoneClick = Date.now() + 600
  }
  const atEdge = (currentPage.value === 0 && dx > 0) || (currentPage.value >= pageCount.value - 1 && dx < 0)
  setTransform(-currentPage.value * pageStep + (atEdge ? dx / 3 : dx), false)
}
function onPointerEnd(e:PointerEvent){
  if (!swipe.active || e.pointerId !== swipe.pointerId) return
  const dx = e.clientX - swipe.startX
  swipe.active = false
  if (!swipe.dragging) return
  const flick = performance.now() - swipe.startTime < 300 && Math.abs(dx) > 30
  const shouldTurn = flick || Math.abs(dx) > pageStep * 0.25
  const target = currentPage.value + (dx < 0 ? 1 : -1)
  if (shouldTurn && target >= 0 && target < pageCount.value) turnPage(dx < 0 ? 'next' : 'prev')
  else goToPage(currentPage.value, true)
}

// ---- 键盘 ----
function onKeydown(event:KeyboardEvent){
  if (loading.value || error.value) return
  if (event.key === 'ArrowLeft' || event.key === 'PageUp') previous()
  if (event.key === 'ArrowRight' || event.key === 'PageDown' || event.key === ' ') next()
  if (event.key === 'Escape' && tocOpen.value) tocOpen.value = false
}

// ---- 进度保存（防抖合并，离开时立即落盘） ----
function queueProgressSave(){
  if (loading.value) return
  if (progressTimer) clearTimeout(progressTimer)
  progressTimer = window.setTimeout(() => {
    progressTimer = 0
    commitProgressSave()
  }, PROGRESS_SAVE_DELAY)
}
function commitProgressSave(){
  progressChain = progressChain.then(() => api(`/api/files/${props.file.id}/book/progress`, {
    method:'PUT', body:JSON.stringify({ page: currentPage.value, total_pages: pageCount.value }), keepalive:true,
  })).catch(e => console.error('保存阅读进度失败', e))
}
function flushProgress(){
  if (loading.value) return
  if (progressTimer) { clearTimeout(progressTimer); progressTimer = 0 }
  commitProgressSave()
}
function flushOnHidden(){ if (document.visibilityState === 'hidden') flushProgress() }
</script>

<template>
  <div ref="rootEl" class="reader-shell" :class="{ dark }">
    <header class="reader-bar" :class="{ 'tools-hidden': toolsHidden }">
      <button class="reader-icon-btn" aria-label="返回" title="返回" @click="emit('close')">‹</button>
      <div class="reader-bar-title"><strong>{{ title }}</strong><small>{{ format.toUpperCase() }}</small></div>
      <button class="reader-icon-btn" aria-label="目录" title="目录" @click="toggleToc">☰</button>
    </header>

    <div ref="viewportEl" class="reader-viewport"
         @pointerdown="onPointerDown" @pointermove="onPointerMove"
         @pointerup="onPointerEnd" @pointercancel="onPointerEnd">
      <div ref="pageEl" class="reader-page"></div>
      <div v-if="!loading && !error" class="reader-zones" aria-hidden="true">
        <div class="reader-zone prev" @click="guardZone(previous)()"></div>
        <div class="reader-zone center" @click="guardZone(toggleTools)()"></div>
        <div class="reader-zone next" @click="guardZone(next)()"></div>
      </div>
      <div v-if="loading" class="reader-loading"><div class="spinner"></div><p>正在打开书页…</p></div>
      <div v-else-if="error" class="reader-loading"><p class="form-error">{{ error }}</p></div>
    </div>

    <footer class="reader-footer" :class="{ 'tools-hidden': toolsHidden }">
      <div class="reader-seek">
        <span class="reader-page-label">{{ pageLabel }}</span>
        <input type="range" :min="0" :max="Math.max(0, pageCount - 1)" :value="currentPage"
               @input="seekTo(Number(($event.target as HTMLInputElement).value))" aria-label="阅读进度">
      </div>
      <div class="reader-actions">
        <button class="reader-action-btn" @click="toggleToc"><b>☰</b>目录</button>
        <button class="reader-action-btn" @click="toggleTheme"><b>{{ dark ? '☀' : '☾' }}</b>{{ dark ? '日间' : '夜间' }}</button>
        <button class="reader-action-btn" @click="fontPopover = !fontPopover"><b>A</b>字号</button>
      </div>
      <div v-if="fontPopover" class="font-popover">
        <button class="font-step" aria-label="缩小字号" @click="stepFont(-1)">A−</button>
        <input type="range" min="14" max="32" :value="fontSize" aria-label="字号"
               @input="setFontSize(Number(($event.target as HTMLInputElement).value))">
        <button class="font-step" aria-label="放大字号" @click="stepFont(1)">A+</button>
      </div>
    </footer>

    <div v-if="tocOpen" class="toc-scrim" @click="tocOpen = false"></div>
    <aside v-if="tocOpen" class="toc-drawer" aria-label="目录">
      <header><strong>目录</strong><button aria-label="关闭目录" @click="tocOpen = false">×</button></header>
      <div class="toc-list">
        <button v-for="(entry, index) in toc" :key="index" class="toc-item" :class="{ active: index === tocActive }" :style="{ paddingLeft: `${14 + Math.min(4, entry.depth || 0) * 16}px` }" @click="jumpToc(index)">
          {{ entry.label }}
        </button>
        <p v-if="!toc.length" class="toc-empty">没有识别到章节目录。</p>
      </div>
    </aside>
  </div>
</template>
