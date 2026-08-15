<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'

const ROOT = '00000000-0000-0000-0000-000000000000'
const FILE_CONCURRENCY = 3
const PART_CONCURRENCY = 4

interface DriveFile { id:string; parent_id:string|null; name:string; kind:'file'|'directory'; size:number; mime_type?:string; etag?:string; status:'pending'|'ready'|'deleting'|'failed'; created_at:string; updated_at:string }
interface UploadTask { id:string; file:File; progress:number; status:'queued'|'uploading'|'done'|'failed'|'cancelled'; error:string; cancelled:boolean; uploadId?:string; requests:XMLHttpRequest[] }
interface FolderOption { id:string; name:string; depth:number }

const user = ref<string|null>(null)
const checking = ref(true)
const login = reactive({ username:'admin', password:'', busy:false, error:'', notice:'' })
const currentId = ref(ROOT)
const current = ref<DriveFile|null>(null)
const items = ref<DriveFile[]>([])
const breadcrumbs = ref<DriveFile[]>([])
const loading = ref(false)
const dragActive = ref(false)
const toast = reactive({ text:'', kind:'error' as 'error'|'success' })
const tasks = reactive<UploadTask[]>([])
const selected = ref<DriveFile|null>(null)
const modal = ref<'rename'|'move'|'preview'|'account'|null>(null)
const renameValue = ref('')
const folders = ref<FolderOption[]>([])
const modalBusy = ref(false)
const account = reactive({ username:'', currentPassword:'', password:'', confirmPassword:'', error:'' })
const fileInput = ref<HTMLInputElement|null>(null)
let activeUploads = 0
let toastTimer = 0

const pathTitle = computed(() => breadcrumbs.value.map(x => x.name || '我的文件').join(' / '))
const unfinished = computed(() => tasks.filter(t => t.status !== 'done' && t.status !== 'cancelled'))

async function api<T>(path:string, init:RequestInit = {}):Promise<T> {
  const headers = new Headers(init.headers)
  if (init.body && !headers.has('Content-Type')) headers.set('Content-Type','application/json')
  const response = await fetch(path, { ...init, headers, credentials:'same-origin' })
  if (!response.ok) {
    let message = `请求失败 (${response.status})`
    try { message = (await response.json()).error?.message || message } catch { /* ignore */ }
    const error = new Error(message) as Error & { status?:number }; error.status=response.status; throw error
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}

function notify(text:string, kind:'error'|'success'='error') { toast.text=text;toast.kind=kind;window.clearTimeout(toastTimer);toastTimer=window.setTimeout(()=>toast.text='',3600) }

async function checkSession() {
  try { const me=await api<{username:string}>('/api/auth/me');user.value=me.username;await openFolder(ROOT) }
  catch { user.value=null }
  finally { checking.value=false }
}
async function submitLogin() {
  login.busy=true;login.error='';login.notice=''
  try { const me=await api<{username:string}>('/api/auth/login',{method:'POST',body:JSON.stringify({username:login.username,password:login.password})});user.value=me.username;login.password='';await openFolder(ROOT) }
  catch(e){login.error=(e as Error).message}
  finally{login.busy=false}
}
async function logout(){await api('/api/auth/logout',{method:'POST'});user.value=null;items.value=[];tasks.splice(0)}
function showAccount(){account.username=user.value||'';account.currentPassword='';account.password='';account.confirmPassword='';account.error='';modal.value='account'}
async function saveAccount(){
  account.error=''
  if(account.password.length<12){account.error='新密码至少需要 12 个字符';return}
  if(account.password!==account.confirmPassword){account.error='两次输入的新密码不一致';return}
  modalBusy.value=true
  try{
    await api('/api/auth/credentials',{method:'PATCH',body:JSON.stringify({current_password:account.currentPassword,username:account.username,password:account.password})})
    modal.value=null;login.username=account.username;login.password='';login.notice='账户已更新，请使用新凭据重新登录';user.value=null;items.value=[];tasks.splice(0)
  }catch(e){account.error=(e as Error).message}
  finally{modalBusy.value=false}
}

async function openFolder(id:string){loading.value=true;try{const [meta,list]=await Promise.all([api<{file:DriveFile;breadcrumbs:DriveFile[]}>(`/api/files/${id}`),api<{items:DriveFile[]}>(`/api/files/${id}/children`)]);currentId.value=id;current.value=meta.file;breadcrumbs.value=meta.breadcrumbs;items.value=list.items;selected.value=null}catch(e){notify((e as Error).message)}finally{loading.value=false}}
async function createFolder(){const name=window.prompt('新文件夹名称');if(!name)return;try{await api('/api/directories',{method:'POST',body:JSON.stringify({parent_id:currentId.value,name})});await openFolder(currentId.value);notify('文件夹已创建','success')}catch(e){notify((e as Error).message)}}
async function removeItem(item:DriveFile){const text=item.kind==='directory'?'仅空文件夹可删除。确定删除吗？':'确定永久删除这个文件吗？';if(!window.confirm(`${text}\n\n${item.name}`))return;try{await api(`/api/files/${item.id}`,{method:'DELETE'});await openFolder(currentId.value);notify('已删除','success')}catch(e){notify((e as Error).message)}}
function showRename(item:DriveFile){selected.value=item;renameValue.value=item.name;modal.value='rename'}
async function saveRename(){if(!selected.value)return;modalBusy.value=true;try{await api(`/api/files/${selected.value.id}`,{method:'PATCH',body:JSON.stringify({name:renameValue.value})});modal.value=null;await openFolder(currentId.value);notify('已重命名','success')}catch(e){notify((e as Error).message)}finally{modalBusy.value=false}}
async function showMove(item:DriveFile){selected.value=item;modalBusy.value=true;modal.value='move';folders.value=[];try{folders.value=await loadFolderTree()}catch(e){notify((e as Error).message);modal.value=null}finally{modalBusy.value=false}}
async function loadFolderTree():Promise<FolderOption[]>{const result:FolderOption[]=[{id:ROOT,name:'我的文件',depth:0}];const queue=[{id:ROOT,depth:0}];while(queue.length){const node=queue.shift()!;const data=await api<{items:DriveFile[]}>(`/api/files/${node.id}/children`);for(const child of data.items.filter(x=>x.kind==='directory'&&x.id!==selected.value?.id)){result.push({id:child.id,name:child.name,depth:node.depth+1});queue.push({id:child.id,depth:node.depth+1})}}return result}
async function moveTo(parentId:string){if(!selected.value)return;modalBusy.value=true;try{await api(`/api/files/${selected.value.id}`,{method:'PATCH',body:JSON.stringify({parent_id:parentId})});modal.value=null;await openFolder(currentId.value);notify('已移动','success')}catch(e){notify((e as Error).message)}finally{modalBusy.value=false}}
function showPreview(item:DriveFile){selected.value=item;modal.value='preview'}
function isImage(item:DriveFile){return ['image/jpeg','image/png','image/webp','image/gif'].includes(item.mime_type||'')}
function download(item:DriveFile){window.location.assign(`/api/files/${item.id}/download`)}

function chooseFiles(){fileInput.value?.click()}
function acceptFiles(list:FileList|File[]){for(const file of Array.from(list)){tasks.push({id:crypto.randomUUID(),file,progress:0,status:'queued',error:'',cancelled:false,requests:[]})}pumpQueue()}
function onDrop(event:DragEvent){dragActive.value=false;if(event.dataTransfer?.files.length)acceptFiles(event.dataTransfer.files)}
function pumpQueue(){while(activeUploads<FILE_CONCURRENCY){const task=tasks.find(t=>t.status==='queued');if(!task)return;activeUploads++;runUpload(task).finally(()=>{activeUploads--;pumpQueue()})}}
async function runUpload(task:UploadTask){task.status='uploading';task.error='';task.cancelled=false;task.progress=0;try{const created=await api<{upload_id:string;mode:'single'|'multipart';url?:string;part_size?:number}>('/api/uploads',{method:'POST',body:JSON.stringify({parent_id:currentId.value,name:task.file.name,size:task.file.size,mime_type:task.file.type||'application/octet-stream'})});task.uploadId=created.upload_id;if(task.cancelled){await abortRemote(task);return}if(created.mode==='single'){await xhrPut(created.url!,task.file,task,(loaded)=>task.progress=percentage(loaded,task.file.size))}else{await multipartUpload(task,created.part_size!)}if(task.cancelled)return;await api(`/api/uploads/${task.uploadId}/complete`,{method:'POST',body:JSON.stringify(created.mode==='multipart'?{parts:(task as UploadTask & {parts?:{part_number:number;etag:string}[]}).parts}:{})});task.progress=100;task.status='done';await openFolder(currentId.value)}catch(e){if(task.cancelled){task.status='cancelled'}else{task.status='failed';task.error=(e as Error).message}}}
async function multipartUpload(task:UploadTask,partSize:number){const count=Math.ceil(task.file.size/partSize);const urls=new Map<number,string>();for(let from=1;from<=count;from+=10){const page=await api<{parts:{part_number:number;url:string}[]}>(`/api/uploads/${task.uploadId}/parts?from=${from}&count=${Math.min(10,count-from+1)}`);page.parts.forEach(p=>urls.set(p.part_number,p.url))}const progress=new Array(count).fill(0) as number[];const completed:{part_number:number;etag:string}[]=[];let cursor=1;const worker=async()=>{while(true){const part=cursor++;if(part>count)return;if(task.cancelled)throw new Error('上传已取消');const start=(part-1)*partSize,end=Math.min(start+partSize,task.file.size),blob=task.file.slice(start,end);const etag=await xhrPut(urls.get(part)!,blob,task,(loaded)=>{progress[part-1]=loaded;task.progress=percentage(progress.reduce((a,b)=>a+b,0),task.file.size)});if(!etag)throw new Error('S3 未返回 ETag，请检查 Bucket CORS 的 ExposeHeaders');completed.push({part_number:part,etag})}};await Promise.all(Array.from({length:Math.min(PART_CONCURRENCY,count)},worker));completed.sort((a,b)=>a.part_number-b.part_number);(task as UploadTask & {parts:{part_number:number;etag:string}[]}).parts=completed}
function xhrPut(url:string,body:Blob,task:UploadTask,onProgress:(n:number)=>void):Promise<string>{return new Promise((resolve,reject)=>{const xhr=new XMLHttpRequest();task.requests.push(xhr);xhr.open('PUT',url);xhr.setRequestHeader('Content-Type',task.file.type||'application/octet-stream');xhr.upload.onprogress=e=>{if(e.lengthComputable)onProgress(e.loaded)};xhr.onload=()=>{task.requests=task.requests.filter(x=>x!==xhr);if(xhr.status>=200&&xhr.status<300)resolve(xhr.getResponseHeader('ETag')||'');else reject(new Error(`S3 上传失败 (${xhr.status})`))};xhr.onerror=()=>reject(new Error('无法连接对象存储，请检查 S3 CORS'));xhr.onabort=()=>reject(new Error('上传已取消'));xhr.send(body)})}
function percentage(done:number,total:number){return total===0?100:Math.min(99,Math.round(done/total*100))}
async function cancelUpload(task:UploadTask){task.cancelled=true;task.requests.forEach(x=>x.abort());await abortRemote(task);task.status='cancelled'}
async function abortRemote(task:UploadTask){if(task.uploadId){try{await api(`/api/uploads/${task.uploadId}`,{method:'DELETE'})}catch{/* stale cleanup retries later */}}}
async function retry(task:UploadTask){await abortRemote(task);task.status='queued';task.error='';task.uploadId=undefined;task.requests=[];task.cancelled=false;pumpQueue()}
function clearFinished(){for(let i=tasks.length-1;i>=0;i--)if(['done','cancelled'].includes(tasks[i].status))tasks.splice(i,1)}
function formatSize(bytes:number){if(bytes===0)return'0 B';const units=['B','KB','MB','GB','TB'];const i=Math.min(Math.floor(Math.log(bytes)/Math.log(1024)),4);return`${(bytes/1024**i).toFixed(i?1:0)} ${units[i]}`}
function formatDate(value:string){const d=new Date(value);return Number.isNaN(d.valueOf())?'—':new Intl.DateTimeFormat('zh-CN',{month:'short',day:'numeric',hour:'2-digit',minute:'2-digit'}).format(d)}

onMounted(checkSession)
</script>

<template>
  <div v-if="checking" class="splash"><div class="brand-mark">C</div><div class="spinner"></div></div>
  <main v-else-if="!user" class="login-page">
    <section class="login-visual"><div class="glow glow-a"></div><div class="glow glow-b"></div><div class="visual-copy"><span class="eyebrow">PRIVATE · DIRECT · YOURS</span><h1>你的文件，<br>安静地待在云上。</h1><p>轻量、自托管，文件直接往返你的 S3。</p></div><div class="cloud-card"><span>☁</span><div><strong>Browser ↔ S3</strong><small>文件内容不经过应用服务器</small></div></div></section>
    <section class="login-panel"><form class="login-form" @submit.prevent="submitLogin"><div class="logo"><span class="brand-mark small">C</span><span>Cloud</span></div><div><p class="eyebrow dark">WELCOME BACK</p><h2>登录私人空间</h2><p class="muted">首次启动的随机凭据可在容器日志中查看</p></div><label>用户名<input v-model="login.username" autocomplete="username" maxlength="128" required></label><label>密码<input v-model="login.password" type="password" autocomplete="current-password" maxlength="1024" required></label><p v-if="login.notice" class="form-success">{{ login.notice }}</p><p v-if="login.error" class="form-error">{{ login.error }}</p><button class="primary wide" :disabled="login.busy">{{ login.busy ? '正在验证…' : '进入我的网盘' }}</button></form></section>
  </main>

  <div v-else class="app-shell" @dragover.prevent="dragActive=true" @dragleave.self="dragActive=false" @drop.prevent="onDrop">
    <header class="topbar"><div class="logo"><span class="brand-mark small">C</span><span>Cloud</span></div><div class="top-actions"><span class="connection"><i></i>S3 直连</span><button class="avatar" :title="`${user} · 账户设置`" @click="showAccount">{{ user.slice(0,1).toUpperCase() }}<span>账户设置</span></button><button class="top-logout" @click="logout">退出</button></div></header>
    <aside class="sidebar"><button class="nav active"><span>▰</span>我的文件</button><div class="sidebar-note"><span>私密空间</span><p>元数据保存在 SQLite，文件内容保存在 S3。</p></div></aside>
    <section class="content">
      <div class="content-head"><div><nav class="breadcrumbs" aria-label="路径"><button v-for="crumb in breadcrumbs" :key="crumb.id" @click="openFolder(crumb.id)">{{ crumb.name || '我的文件' }}<span>/</span></button></nav><h1>{{ current?.name || '我的文件' }}</h1><p>{{ items.length }} 个项目 · {{ pathTitle }}</p></div><div class="actions"><button class="secondary" @click="createFolder">＋ 新建文件夹</button><button class="primary" @click="chooseFiles">↑ 上传文件</button><input ref="fileInput" hidden type="file" multiple @change="e=>{const el=e.target as HTMLInputElement;if(el.files)acceptFiles(el.files);el.value=''}"></div></div>
      <div v-if="loading" class="state"><div class="spinner"></div><p>正在读取文件…</p></div>
      <div v-else-if="!items.length" class="state empty"><div class="empty-icon">⌁</div><h3>这里还是空的</h3><p>拖放文件到这里，或创建第一个文件夹。</p><button class="primary" @click="chooseFiles">上传文件</button></div>
      <div v-else class="file-table"><div class="table-head"><span>名称</span><span>大小</span><span>修改时间</span><span></span></div><div v-for="item in items" :key="item.id" class="file-row" :class="{mutedrow:item.status!=='ready'}" @dblclick="item.kind==='directory'?openFolder(item.id):(isImage(item)&&showPreview(item))"><div class="file-name"><button class="file-icon" :class="item.kind" @click="item.kind==='directory'?openFolder(item.id):(isImage(item)&&showPreview(item))">{{ item.kind==='directory'?'▰':isImage(item)?'◈':'◇' }}</button><div><strong>{{ item.name }}</strong><small v-if="item.status!=='ready'">{{ item.status }}</small><small v-else>{{ item.kind==='directory'?'文件夹':item.mime_type||'文件' }}</small></div></div><span>{{ item.kind==='directory'?'—':formatSize(item.size) }}</span><span>{{ formatDate(item.updated_at) }}</span><div class="row-actions"><button v-if="item.kind==='file'" title="下载" @click="download(item)">↓</button><button title="重命名" @click="showRename(item)">✎</button><button title="移动" @click="showMove(item)">↗</button><button title="删除" class="danger" @click="removeItem(item)">×</button></div></div></div>
    </section>

    <div v-if="dragActive" class="drop-zone"><div><span>↓</span><h2>释放以上传到 {{ current?.name || '我的文件' }}</h2><p>文件将直接发送到 S3</p></div></div>
    <section v-if="tasks.length" class="upload-panel"><header><div><strong>上传</strong><span v-if="unfinished.length">{{ unfinished.length }} 项进行中</span></div><button @click="clearFinished">清除已完成</button></header><div class="task-list"><article v-for="task in tasks" :key="task.id"><div class="task-top"><span class="task-icon">↑</span><div><strong>{{ task.file.name }}</strong><small>{{ formatSize(task.file.size) }} · {{ task.status==='queued'?'等待中':task.status==='uploading'?'正在上传':task.status==='done'?'已完成':task.status==='cancelled'?'已取消':task.error }}</small></div><b>{{ task.progress }}%</b><button v-if="task.status==='queued'||task.status==='uploading'" @click="cancelUpload(task)">×</button><button v-else-if="task.status==='failed'" @click="retry(task)">重试</button></div><div class="progress"><i :class="task.status" :style="{width:`${task.progress}%`}"></i></div></article></div></section>

    <div v-if="modal" class="modal-backdrop" @click.self="modal=null"><section v-if="modal==='rename'" class="modal"><header><div><p class="eyebrow dark">EDIT</p><h2>重命名</h2></div><button @click="modal=null">×</button></header><label>新名称<input v-model="renameValue" maxlength="1024" @keyup.enter="saveRename"></label><footer><button class="secondary" @click="modal=null">取消</button><button class="primary" :disabled="modalBusy" @click="saveRename">保存</button></footer></section><section v-else-if="modal==='move'" class="modal folder-modal"><header><div><p class="eyebrow dark">MOVE</p><h2>移动「{{ selected?.name }}」</h2></div><button @click="modal=null">×</button></header><div v-if="modalBusy" class="state small"><div class="spinner"></div></div><div v-else class="folder-list"><button v-for="folder in folders" :key="folder.id" :style="{paddingLeft:`${18+folder.depth*22}px`}" @click="moveTo(folder.id)"><span>▰</span>{{ folder.name }}</button></div></section><section v-else-if="modal==='account'" class="modal account-modal"><header><div><p class="eyebrow dark">SECURITY</p><h2>账户设置</h2></div><button @click="modal=null">×</button></header><form @submit.prevent="saveAccount"><p class="modal-hint">修改后会退出所有已登录设备，请使用新凭据重新登录。</p><label>管理员用户名<input v-model="account.username" autocomplete="username" maxlength="128" required></label><label>当前密码<input v-model="account.currentPassword" type="password" autocomplete="current-password" maxlength="1024" required></label><label>新密码<input v-model="account.password" type="password" autocomplete="new-password" minlength="12" maxlength="1024" required></label><label>确认新密码<input v-model="account.confirmPassword" type="password" autocomplete="new-password" minlength="12" maxlength="1024" required></label><p v-if="account.error" class="form-error">{{ account.error }}</p><footer><button type="button" class="secondary" @click="modal=null">取消</button><button class="primary" :disabled="modalBusy">{{ modalBusy?'正在保存…':'更新并退出' }}</button></footer></form></section><section v-else class="preview-modal"><header><strong>{{ selected?.name }}</strong><button @click="modal=null">×</button></header><img v-if="selected" :src="`/api/files/${selected.id}/preview`" :alt="selected.name"><footer><button class="primary" @click="selected&&download(selected)">↓ 下载原图</button></footer></section></div>
    <div v-if="toast.text" class="toast" :class="toast.kind">{{ toast.text }}</div>
  </div>
</template>
