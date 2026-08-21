<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { ReaderApp } from './reader-app'
import type { DriveFile } from './api'

// 阅读器外壳：渲染原版 reader 的静态骨架，交给 ReaderApp（reader.js 的
// 原样移植）用命令式 DOM 接管全部逻辑——分栏分页、热区点击、滑动、进度。
const props = defineProps<{ file: DriveFile }>()
const emit = defineEmits<{ (e:'close'):void }>()
const rootEl = ref<HTMLElement|null>(null)

const kind = /\.epub$/i.test(props.file.name) ? 'epub' : 'txt'

onMounted(() => {
  ReaderApp.init(document.body)
  ReaderApp.openBook(props.file.id, {
    embedded: true,
    book: { id: props.file.id, title: props.file.name, kind },
    onExit: () => emit('close'),
  })
})
onBeforeUnmount(() => {
  ReaderApp.saveProgress()
  ReaderApp.hide()
  document.title = 'revaro · 私人网盘'
})
</script>

<template>
  <section id="reader-view" ref="rootEl" class="reader-shell">
    <header class="reader-bar">
      <button id="reader-back" class="reader-icon-btn" aria-label="返回"><svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m15 18-6-6 6-6"/></svg></button>
      <div class="reader-bar-title"><strong id="reader-title">正在打开…</strong><small id="reader-kind"></small></div>
      <button class="reader-icon-btn" id="toc-button" aria-label="打开目录" aria-expanded="false">☰</button>
    </header>
    <main id="viewport" class="reader-viewport">
      <div id="loading" class="reader-loading">正在打开书页…</div>
      <div id="reader-page" class="reader-page"></div>
      <button id="prev-zone" class="page-zone prev-zone" aria-label="上一页"></button>
      <button id="center-zone" class="page-zone center-zone" aria-label="显示或隐藏工具栏"></button>
      <button id="next-zone" class="page-zone next-zone" aria-label="下一页"></button>
    </main>
    <div id="toc-scrim" class="toc-scrim hidden"></div>
    <aside id="toc-drawer" class="toc-drawer" aria-label="书籍目录" aria-hidden="true">
      <div class="toc-heading"><div><small>CONTENTS</small><h2>目录</h2></div><button id="toc-close" aria-label="关闭目录">×</button></div>
      <nav id="toc-list" class="toc-list"></nav>
    </aside>
    <div id="font-popover" class="font-popover hidden">
      <span>字号</span>
      <button id="font-smaller" class="font-step" aria-label="减小字号">A−</button>
      <input id="font-slider" type="range" min="14" max="32" step="1" value="19" aria-label="阅读字号">
      <button id="font-larger" class="font-step" aria-label="增大字号">A+</button>
    </div>
    <footer class="reader-footer">
      <div class="reader-seek">
        <span id="page-label">— / — · 0%</span>
        <input id="page-slider" type="range" min="0" max="0" value="0" step="1" aria-label="页面进度">
      </div>
      <div class="reader-actions">
        <button id="toc-button-2" class="reader-action-btn"><b>☰</b><span>目录</span></button>
        <button id="font-button" class="reader-action-btn"><b>A</b><span>字号</span></button>
        <button id="theme-button" class="reader-action-btn"><b>◐</b><span>明暗</span></button>
      </div>
    </footer>
  </section>
</template>
