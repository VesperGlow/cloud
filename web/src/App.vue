<script setup lang="ts">
import DOMPurify from 'dompurify'
import { marked } from 'marked'
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { api } from './api'
import type { DriveFile } from './api'
import ReaderView from './Reader.vue'

const ROOT = '00000000-0000-0000-0000-000000000000'
const FILE_CONCURRENCY = 3
const BLOCK_PUT_CONCURRENCY = 4
const BLOCK_REGISTER_BATCH = 1000
const COMPLETE_RETRIES = 3

interface UploadTask { id:string; file:File; progress:number; status:'queued'|'uploading'|'done'|'failed'|'cancelled'; error:string; cancelled:boolean; uploadId?:string; requests:XMLHttpRequest[] }
interface FolderOption { id:string; name:string; depth:number }
interface ShareResponse { active:boolean; url?:string; created_at?:string }
interface ProfileResponse { username:string; has_avatar:boolean }
interface StorageStats { total_bytes:number; file_count:number }

const user = ref<string|null>(null)
const hasAvatar = ref(false)
const avatarVersion = ref(Date.now())
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
type ModalName = 'rename'|'move'|'preview'|'share'|'account'|'editor'|'reader'
const modal = ref<ModalName|null>(null)
const readerFile = ref<DriveFile|null>(null)
const renameValue = ref('')
const folders = ref<FolderOption[]>([])
const modalBusy = ref(false)
const account = reactive({ username:'', currentPassword:'', password:'', confirmPassword:'', error:'' })
const avatar = reactive({ busy:false, error:'' })
const share = reactive({ active:false, url:'', createdAt:'', busy:false, error:'', copied:false })
const editor = reactive({ isNew:false, fileId:'', name:'', originalName:'', content:'', original:'', etag:'', mode:'edit' as 'edit'|'split'|'preview', busy:false, error:'' })
const storageStats = reactive<StorageStats>({ total_bytes:0, file_count:0 })
const fileInput = ref<HTMLInputElement|null>(null)
const avatarInput = ref<HTMLInputElement|null>(null)
const viewMode = ref<'list'|'grid'>('list')
let activeUploads = 0
let toastTimer = 0

const pathTitle = computed(() => breadcrumbs.value.map(x => x.name || '我的文件').join(' / '))
const unfinished = computed(() => tasks.filter(t => t.status !== 'done' && t.status !== 'cancelled'))
const editorDirty = computed(() => editor.content !== editor.original || editor.name !== editor.originalName)
const editorBytes = computed(() => new Blob([editor.content]).size)
const editorIsMarkdown = computed(() => /\.(md|markdown)$/i.test(editor.name))
const renderedMarkdown = computed(() => DOMPurify.sanitize(marked.parse(editor.content, { async:false }) as string))
const avatarURL = computed(() => `/api/profile/avatar?v=${avatarVersion.value}`)
const galleryItems = computed(() => items.value.filter(item => isImage(item)||isVideo(item)))
const galleryIndex = computed(() => selected.value ? galleryItems.value.findIndex(item => item.id===selected.value?.id) : -1)
const hasGalleryNavigation = computed(() => galleryIndex.value>=0&&galleryItems.value.length>1)
const previewDownloadLabel = computed(() => selected.value ? isImage(selected.value)?'下载原图':isVideo(selected.value)?'下载视频':isAudio(selected.value)?'下载音频':'下载文件' : '下载文件')

function notify(text:string, kind:'error'|'success'='error') { toast.text=text;toast.kind=kind;window.clearTimeout(toastTimer);toastTimer=window.setTimeout(()=>toast.text='',3600) }

function isBook(item:DriveFile){return item.kind==='file'&&item.status==='ready'&&/\.(epub|txt)$/i.test(item.name)}
function openReader(item:DriveFile){readerFile.value=item;openModal('reader');history.replaceState({cloudNav:true},'','/read/'+item.id)}
async function checkSession() {
  try { const me=await api<ProfileResponse>('/api/auth/me');user.value=me.username;hasAvatar.value=me.has_avatar;await openRoute() }
  catch { user.value=null;hasAvatar.value=false }
  finally { checking.value=false }
}
// 启动路由：按 URL 恢复到对应文件夹（/f/{id}），再处理阅读器深链。
async function openRoute(){
  const fm=location.pathname.match(/^\/f\/([^/]+)\/?$/)
  suppressHistory=true
  try{
    if(fm){
      const id=decodeURIComponent(fm[1])
      await openFolder(id)
      if(currentId.value!==id)history.replaceState({cloudNav:true},'','/')
    }else{
      await openFolder(ROOT)
    }
  }finally{suppressHistory=false}
  openDeepLink()
}
// 深链 /read/{fileId}：登录后直接打开阅读器。
async function openDeepLink(){
  const m=location.pathname.match(/^\/read\/([^/]+)\/?$/)
  if(!m)return
  history.replaceState({cloudNav:true},'',folderURL(currentId.value))
  try{
    const data=await api<{file:DriveFile}>(`/api/files/${decodeURIComponent(m[1])}`)
    if(data.file.kind==='file'&&isBook(data.file))openReader(data.file)
  }catch{/* 文件不存在或不可读：留在当前文件夹 */}
}
async function submitLogin() {
  login.busy=true;login.error='';login.notice=''
  try { const me=await api<ProfileResponse>('/api/auth/login',{method:'POST',body:JSON.stringify({username:login.username,password:login.password})});user.value=me.username;hasAvatar.value=me.has_avatar;login.password='';await openFolder(ROOT) }
  catch(e){login.error=(e as Error).message}
  finally{login.busy=false}
}
async function logout(){await api('/api/auth/logout',{method:'POST'});user.value=null;hasAvatar.value=false;items.value=[];tasks.splice(0)}
function showAccount(){account.username=user.value||'';account.currentPassword='';account.password='';account.confirmPassword='';account.error='';avatar.error='';openModal('account')}
function chooseAvatar(){avatarInput.value?.click()}
async function uploadAvatar(file:File){
  avatar.error=''
  if(!['image/jpeg','image/png','image/gif','image/webp'].includes(file.type)){avatar.error='请选择 JPG、PNG、GIF 或 WebP 图片';return}
  if(file.size>2*1024*1024){avatar.error='头像不能超过 2 MiB';return}
  avatar.busy=true
  try{
    const dataURL=await new Promise<string>((resolve,reject)=>{const reader=new FileReader();reader.onload=()=>resolve(String(reader.result));reader.onerror=()=>reject(new Error('无法读取图片'));reader.readAsDataURL(file)})
    await api('/api/profile/avatar',{method:'PUT',body:JSON.stringify({data_url:dataURL})})
    hasAvatar.value=true;avatarVersion.value=Date.now();notify('头像已更新','success')
  }catch(e){avatar.error=(e as Error).message}
  finally{avatar.busy=false}
}
async function removeAvatar(){
  avatar.error='';avatar.busy=true
  try{await api('/api/profile/avatar',{method:'DELETE'});hasAvatar.value=false;avatarVersion.value=Date.now();notify('头像已移除','success')}
  catch(e){avatar.error=(e as Error).message}
  finally{avatar.busy=false}
}
async function saveAccount(){
  account.error=''
  if(account.password.length<12){account.error='新密码至少需要 12 个字符';return}
  if(account.password!==account.confirmPassword){account.error='两次输入的新密码不一致';return}
  modalBusy.value=true
  try{
    await api('/api/auth/credentials',{method:'PATCH',body:JSON.stringify({current_password:account.currentPassword,username:account.username,password:account.password})})
    closeModal();login.username=account.username;login.password='';login.notice='账户已更新，请使用新凭据重新登录';user.value=null;items.value=[];tasks.splice(0)
  }catch(e){account.error=(e as Error).message}
  finally{modalBusy.value=false}
}

function folderURL(id:string){return id===ROOT?'/':'/f/'+id}
async function openFolder(id:string){loading.value=true;try{const [meta,list,stats]=await Promise.all([api<{file:DriveFile;breadcrumbs:DriveFile[]}>(`/api/files/${id}`),api<{items:DriveFile[]}>(`/api/files/${id}/children`),api<StorageStats>('/api/storage/stats')]);if(!suppressHistory&&id!==currentId.value){navActions.value.push({kind:'folder',id:currentId.value});window.history.pushState({cloudNav:true},'')}currentId.value=id;current.value=meta.file;breadcrumbs.value=meta.breadcrumbs;items.value=list.items;storageStats.total_bytes=stats.total_bytes;storageStats.file_count=stats.file_count;selected.value=null;history.replaceState({cloudNav:true},'',folderURL(id))}catch(e){notify((e as Error).message)}finally{loading.value=false}}

// 应用内导航历史：每次进入文件夹/打开弹窗都 pushState，系统返回键先关
// 弹窗、再逐级返回上一屏，而不是直接退出整个应用。
type NavAction={kind:'folder';id:string}|{kind:'modal-close'}
const navActions=ref<NavAction[]>([])
let suppressHistory=false
let popChain:Promise<void>=Promise.resolve()
function handlePopState(){
  const action=navActions.value.pop()
  if(!action)return
  if(action.kind==='modal-close'){
    if(modal.value==='reader')history.replaceState({cloudNav:true},'',folderURL(currentId.value))
    modal.value=null;return
  }
  popChain=popChain.then(async()=>{
    suppressHistory=true
    try{await openFolder(action.id)}finally{suppressHistory=false}
  })
}
function openModal(name:ModalName){
  if(!modal.value){navActions.value.push({kind:'modal-close'});window.history.pushState({cloudNav:true},'')}
  modal.value=name
}
function closeModal(){if(modal.value)window.history.back()}
function goUp(){const parent=current.value?.parent_id;if(parent)openFolder(parent)}
async function createFolder(){const name=window.prompt('新文件夹名称');if(!name)return;try{await api('/api/directories',{method:'POST',body:JSON.stringify({parent_id:currentId.value,name})});await openFolder(currentId.value);notify('文件夹已创建','success')}catch(e){notify((e as Error).message)}}
async function removeItem(item:DriveFile){const text=item.kind==='directory'?'仅空文件夹可删除。确定删除吗？':'确定永久删除这个文件吗？';if(!window.confirm(`${text}\n\n${item.name}`))return;try{await api(`/api/files/${item.id}`,{method:'DELETE'});await openFolder(currentId.value);notify('已删除','success')}catch(e){notify((e as Error).message)}}
function showRename(item:DriveFile){selected.value=item;renameValue.value=item.name;openModal('rename')}
async function saveRename(){if(!selected.value)return;modalBusy.value=true;try{await api(`/api/files/${selected.value.id}`,{method:'PATCH',body:JSON.stringify({name:renameValue.value})});closeModal();await openFolder(currentId.value);notify('已重命名','success')}catch(e){notify((e as Error).message)}finally{modalBusy.value=false}}
async function showMove(item:DriveFile){selected.value=item;modalBusy.value=true;openModal('move');folders.value=[];try{folders.value=await loadFolderTree()}catch(e){notify((e as Error).message);closeModal()}finally{modalBusy.value=false}}
async function loadFolderTree():Promise<FolderOption[]>{const result:FolderOption[]=[{id:ROOT,name:'我的文件',depth:0}];const queue=[{id:ROOT,depth:0}];while(queue.length){const node=queue.shift()!;const data=await api<{items:DriveFile[]}>(`/api/files/${node.id}/children`);for(const child of data.items.filter(x=>x.kind==='directory'&&x.id!==selected.value?.id)){result.push({id:child.id,name:child.name,depth:node.depth+1});queue.push({id:child.id,depth:node.depth+1})}}return result}
async function moveTo(parentId:string){if(!selected.value)return;modalBusy.value=true;try{await api(`/api/files/${selected.value.id}`,{method:'PATCH',body:JSON.stringify({parent_id:parentId})});closeModal();await openFolder(currentId.value);notify('已移动','success')}catch(e){notify((e as Error).message)}finally{modalBusy.value=false}}
function showPreview(item:DriveFile){selected.value=item;openModal('preview')}
async function showShare(item:DriveFile){selected.value=item;openModal('share');share.active=false;share.url='';share.createdAt='';share.error='';share.copied=false;share.busy=true;try{const data=await api<ShareResponse>(`/api/files/${item.id}/share`);share.active=data.active;share.url=data.url||'';share.createdAt=data.created_at||''}catch(e){share.error=(e as Error).message}finally{share.busy=false}}
async function createShare(replace=false){if(!selected.value)return;if(replace&&!window.confirm('重新生成后，旧分享链接会立即失效。继续吗？'))return;share.busy=true;share.error='';share.copied=false;try{const data=await api<ShareResponse>(`/api/files/${selected.value.id}/share`,{method:'POST'});share.active=data.active;share.url=data.url||'';share.createdAt=data.created_at||''}catch(e){share.error=(e as Error).message}finally{share.busy=false}}
async function revokeShare(){if(!selected.value||!window.confirm('停止分享后，现有订阅链接会立即失效。继续吗？'))return;share.busy=true;share.error='';try{await api(`/api/files/${selected.value.id}/share`,{method:'DELETE'});share.active=false;share.url='';share.createdAt='';share.copied=false;notify('分享已停止','success')}catch(e){share.error=(e as Error).message}finally{share.busy=false}}
async function copyShare(){if(!share.url)return;try{await navigator.clipboard.writeText(share.url);share.copied=true;window.setTimeout(()=>share.copied=false,2200)}catch{share.error='复制失败，请手动选择链接复制'}}
function isImage(item:DriveFile){return item.kind==='file'&&item.status==='ready'&&(['image/jpeg','image/png','image/webp','image/gif','image/avif'].includes(item.mime_type||'')||/\.(jpe?g|png|gif|webp|avif)$/i.test(item.name))}
function isVideo(item:DriveFile){return item.kind==='file'&&item.status==='ready'&&((item.mime_type||'').startsWith('video/')||/\.(mp4|webm|ogv|mov|m4v)$/i.test(item.name))}
function isAudio(item:DriveFile){return item.kind==='file'&&item.status==='ready'&&((item.mime_type||'').startsWith('audio/')||/\.(mp3|wav|ogg|oga|m4a|aac|flac)$/i.test(item.name))}
function isMedia(item:DriveFile){return isImage(item)||isVideo(item)||isAudio(item)}
function isEditable(item:DriveFile){return item.kind==='file'&&item.status==='ready'&&item.size<=1024*1024&&/\.(md|markdown|txt|ya?ml|json|toml|ini|conf|log|csv)$/i.test(item.name)}
function previewURL(item:DriveFile){return `/api/files/${item.id}/preview`}
function openItem(item:DriveFile){if(item.kind==='directory')openFolder(item.id);else if(isBook(item)&&(!isEditable(item)||/\.epub$/i.test(item.name)))openReader(item);else if(isEditable(item))openEditor(item);else if(isMedia(item))showPreview(item)}
function changePreview(direction:-1|1){if(!hasGalleryNavigation.value)return;const next=(galleryIndex.value+direction+galleryItems.value.length)%galleryItems.value.length;selected.value=galleryItems.value[next]}
function handlePreviewKey(event:KeyboardEvent){if(modal.value!=='preview')return;if(event.key==='ArrowLeft'||event.key==='ArrowRight'){event.preventDefault();changePreview(event.key==='ArrowLeft'?-1:1)}}

// 手机端左右滑动切换上一张/下一张（参考 suzuka 的灯箱手势）：水平位移
// 超过阈值且横轴占优时切图，跟手拖拽带过渡回弹。
const swipe=reactive({active:false,pointerId:0,startX:0,startY:0,dx:0,dy:0})
const stageEl=ref<HTMLElement|null>(null)
let swipeStartedOnStage=false
const stageSwipeable=computed(()=>{const item=selected.value;return !!item&&isImage(item)&&hasGalleryNavigation.value})
const swipeStyle=computed(()=>({
  transform:`translateX(${swipe.active?swipe.dx:0}px)${swipe.active?' scale(.985)':''}`,
  transition:swipe.active?'none':'transform .24s cubic-bezier(.22,.8,.3,1)',
}))
function onStagePointerDown(e:PointerEvent){
  if(modal.value!=='preview')return
  // 上/下一张按钮自己处理点击，不要被滑动手势的 pointer capture 劫持
  if(e.target instanceof Element&&e.target.closest('.preview-nav'))return
  if(e.pointerType==='mouse'&&e.button!==0)return
  // 所有媒体类型都记录"按下起点是否在舞台空白处"，用于点击空白关闭预览
  swipeStartedOnStage=e.target===stageEl.value
  if(!stageSwipeable.value)return // 视频/音频/单图：不接管指针，交给点击判定
  if(swipe.active)return // 多点触控时放弃滑动手势
  swipe.active=true;swipe.pointerId=e.pointerId;swipe.startX=e.clientX;swipe.startY=e.clientY;swipe.dx=0;swipe.dy=0
  stageEl.value?.setPointerCapture(e.pointerId)
}
function onStagePointerMove(e:PointerEvent){
  if(!swipe.active||e.pointerId!==swipe.pointerId)return
  swipe.dx=e.clientX-swipe.startX;swipe.dy=e.clientY-swipe.startY
}
function onStagePointerEnd(e:PointerEvent){
  if(!swipe.active||e.pointerId!==swipe.pointerId)return
  const dx=swipe.dx,dy=swipe.dy
  swipe.active=false;swipe.dx=0;swipe.dy=0
  if(Math.abs(dx)>60&&Math.abs(dx)>Math.abs(dy)*1.25)changePreview(dx<0?1:-1)
}
// 点击舞台空白处关闭预览：图片（pointer capture 会把点击重定向到舞台）、
// 视频、音频、单张图片通用；点媒体本体或按钮都不会误触发退出。
function onStageClick(e:MouseEvent){
  if(swipeStartedOnStage&&e.target===stageEl.value)closeModal()
  swipeStartedOnStage=false
}
function newDocument(){selected.value=null;editor.isNew=true;editor.fileId='';editor.name='未命名文档.md';editor.originalName=editor.name;editor.content='';editor.original='';editor.etag='';editor.mode='edit';editor.busy=false;editor.error='';openModal('editor')}
async function openEditor(item:DriveFile){
  selected.value=item;editor.isNew=false;editor.fileId=item.id;editor.name=item.name;editor.originalName=item.name;editor.content='';editor.original='';editor.etag='';editor.mode='edit';editor.error='';editor.busy=true;openModal('editor')
  try{const data=await api<{content:string;etag:string}>(`/api/files/${item.id}/content`);editor.content=data.content;editor.original=data.content;editor.etag=data.etag||''}
  catch(e){editor.error=(e as Error).message}
  finally{editor.busy=false}
}
async function saveDocument(){
  editor.error=''
  if(editor.isNew&&!editor.name.trim()){editor.error='请输入文件名';return}
  if(!/\.(md|markdown|txt|ya?ml|json|toml|ini|conf|log|csv)$/i.test(editor.name)){editor.error='支持 Markdown、TXT、YAML、JSON、TOML、INI、CONF、LOG 和 CSV';return}
  if(editorBytes.value>1024*1024){editor.error='可编辑文档不能超过 1 MiB';return}
  editor.busy=true
  try{
    const saved=editor.isNew
      ?await api<DriveFile>('/api/documents',{method:'POST',body:JSON.stringify({parent_id:currentId.value,name:editor.name.trim(),content:editor.content})})
      :await api<DriveFile>(`/api/files/${editor.fileId}/content`,{method:'PUT',body:JSON.stringify({content:editor.content,etag:editor.etag})})
    editor.isNew=false;editor.fileId=saved.id;editor.name=saved.name;editor.originalName=saved.name;editor.etag=saved.etag||'';editor.original=editor.content;selected.value=saved
    await openFolder(currentId.value);notify('文档已保存','success')
  }catch(e){editor.error=(e as Error).message}
  finally{editor.busy=false}
}
function closeEditor(){if(editorDirty.value&&!window.confirm('还有未保存的修改，确定关闭吗？'))return;closeModal()}
function closeBackdrop(){if(modal.value==='editor')closeEditor();else closeModal()}
function hideBrokenImage(event:Event){(event.target as HTMLImageElement).hidden=true}
function setViewMode(mode:'list'|'grid'){viewMode.value=mode;selected.value=null;localStorage.setItem('cloud-view-mode',mode)}
function toggleSelection(item:DriveFile){selected.value=selected.value?.id===item.id?null:item}
function clearSelectionFromBlank(event:MouseEvent){
  if(!selected.value||modal.value)return
  const target=event.target
  if(!(target instanceof Element)||target.closest('button,a,input,textarea,select,[role="toolbar"],.file-card,.file-row'))return
  selected.value=null
}
function download(item:DriveFile){window.location.assign(`/api/files/${item.id}/download`)}

function chooseFiles(){fileInput.value?.click()}
function acceptFiles(list:FileList|File[]){for(const file of Array.from(list)){tasks.push({id:crypto.randomUUID(),file,progress:0,status:'queued',error:'',cancelled:false,requests:[]})}pumpQueue()}
function onDrop(event:DragEvent){dragActive.value=false;if(event.dataTransfer?.files.length)acceptFiles(event.dataTransfer.files)}
function pumpQueue(){while(activeUploads<FILE_CONCURRENCY){const task=tasks.find(t=>t.status==='queued');if(!task)return;activeUploads++;runUpload(task).finally(()=>{activeUploads--;pumpQueue()})}}
interface BlockSpec { id:string; size:number; offset:number }
interface RegisteredBlock { id:string; size:number; exists:boolean; url?:string; offset:number }

async function runUpload(task:UploadTask){
  task.status='uploading';task.error='';task.cancelled=false;task.progress=0
  try{
    const created=await api<{upload_id:string;mode:'blocks';block_size:number;block_count:number}>('/api/uploads',{method:'POST',body:JSON.stringify({parent_id:currentId.value,name:task.file.name,size:task.file.size,mime_type:task.file.type||'application/octet-stream'})})
    task.uploadId=created.upload_id
    if(task.cancelled){await abortRemote(task);return}
    // 1) 按块切分文件并计算每个块的 SHA-256（内容寻址，重复块自动去重）。
    const blocks=await hashBlocks(task,created.block_size,created.block_count)
    if(task.cancelled){await abortRemote(task);return}
    // 2) 登记全部块；服务端为缺失的块签发条件 PUT 的预签名 URL。
    const registered=await registerBlocks(task,created.upload_id,blocks)
    if(task.cancelled){await abortRemote(task);return}
    // 3) 只把缺失的块直传到 S3。
    await uploadBlocks(task,registered.filter(b=>!b.exists&&b.url))
    if(task.cancelled){await abortRemote(task);return}
    // 4) 完成上传；409 时按缺失列表修复并重试。
    await completeWithRepair(task,created.upload_id,blocks)
    task.progress=100;task.status='done';await openFolder(currentId.value);scheduleAutoClear()
  }catch(e){if(task.cancelled){task.status='cancelled';scheduleAutoClear()}else{task.status='failed';task.error=(e as Error).message}}
}

async function sha256Hex(blob:Blob):Promise<string>{
  const digest=await crypto.subtle.digest('SHA-256',await blob.arrayBuffer())
  return Array.from(new Uint8Array(digest)).map(b=>b.toString(16).padStart(2,'0')).join('')
}

async function hashBlocks(task:UploadTask,blockSize:number,count:number):Promise<BlockSpec[]>{
  const blocks:BlockSpec[]=[]
  let hashed=0
  for(let i=0;i<count;i++){
    if(task.cancelled)throw new Error('上传已取消')
    const start=i*blockSize,end=Math.min(start+blockSize,task.file.size)
    const id=await sha256Hex(task.file.slice(start,end))
    blocks.push({id,size:end-start,offset:start})
    hashed+=end-start
    task.progress=Math.floor(percentage(hashed,task.file.size)*0.35)
  }
  return blocks
}

async function registerBlocks(task:UploadTask,uploadId:string,blocks:BlockSpec[]):Promise<RegisteredBlock[]>{
  const out:RegisteredBlock[]=[]
  for(let from=0;from<blocks.length;from+=BLOCK_REGISTER_BATCH){
    const page=blocks.slice(from,from+BLOCK_REGISTER_BATCH)
    const data=await api<{blocks:{id:string;size:number;exists:boolean;url?:string}[]}>(`/api/uploads/${uploadId}/blocks`,{method:'POST',body:JSON.stringify({blocks:page.map(b=>({id:b.id,size:b.size}))})})
    // 服务端按顺序回显；把文件偏移重新挂回每个块。
    data.blocks.forEach((b,i)=>out.push({...b,offset:page[i].offset}))
  }
  return out
}

async function uploadBlocks(task:UploadTask,blocks:RegisteredBlock[]){
  const total=blocks.reduce((sum,b)=>sum+b.size,0)
  const sent=new Array(blocks.length).fill(0) as number[]
  let cursor=0
  const worker=async()=>{
    while(true){
      const idx=cursor++
      if(idx>=blocks.length)return
      if(task.cancelled)throw new Error('上传已取消')
      const b=blocks[idx]
      const blob=task.file.slice(b.offset,b.offset+b.size)
      await xhrPutBlock(b.url!,blob,task,(loaded)=>{sent[idx]=loaded;task.progress=35+Math.floor(percentage(sent.reduce((a,x)=>a+x,0),total)*0.64)})
    }
  }
  await Promise.all(Array.from({length:Math.min(BLOCK_PUT_CONCURRENCY,blocks.length)},worker))
}

async function completeWithRepair(task:UploadTask,uploadId:string,blocks:BlockSpec[]){
  for(let attempt=0;attempt<COMPLETE_RETRIES;attempt++){
    if(task.cancelled)throw new Error('上传已取消')
    try{
      // 服务端只认 {id,size}：offset 是前端本地字段，不能带上
      await api(`/api/uploads/${uploadId}/complete`,{method:'POST',body:JSON.stringify({blocks:blocks.map(b=>({id:b.id,size:b.size}))})})
      return
    }catch(e){
      const err=e as Error & {status?:number;data?:unknown}
      const missing:string[]|undefined=(err.data as {error?:{missing_blocks?:string[]}}|null)?.error?.missing_blocks
      if(err.status!==409||!missing?.length)throw e
      // 有块在登记后被回收（极端竞态）：重新登记拿到新 URL，补传后重试。
      const ids=new Set(missing)
      const registered=await registerBlocks(task,uploadId,blocks.filter(b=>ids.has(b.id)))
      await uploadBlocks(task,registered.filter(b=>!b.exists&&b.url))
    }
  }
  throw new Error('无法完成块校验，请重试')
}

function xhrPutBlock(url:string,body:Blob,task:UploadTask,onProgress:(n:number)=>void):Promise<void>{
  return new Promise((resolve,reject)=>{
    const xhr=new XMLHttpRequest()
    task.requests.push(xhr)
    xhr.open('PUT',url)
    xhr.setRequestHeader('Content-Type','application/octet-stream')
    xhr.setRequestHeader('If-None-Match','*')
    xhr.upload.onprogress=e=>{if(e.lengthComputable)onProgress(e.loaded)}
    xhr.onload=()=>{
      task.requests=task.requests.filter(x=>x!==xhr)
      if(xhr.status>=200&&xhr.status<300)resolve()
      else if(xhr.status===412)resolve() // 内容相同的块已存在（并发去重），视为成功
      else reject(new Error(`S3 块上传失败 (${xhr.status})`))
    }
    xhr.onerror=()=>reject(new Error('无法连接对象存储，请检查 S3 CORS'))
    xhr.onabort=()=>reject(new Error('上传已取消'))
    xhr.send(body)
  })
}
function percentage(done:number,total:number){return total===0?100:Math.min(99,Math.round(done/total*100))}
async function cancelUpload(task:UploadTask){task.cancelled=true;task.requests.forEach(x=>x.abort());await abortRemote(task);task.status='cancelled'}
async function abortRemote(task:UploadTask){if(task.uploadId){try{await api(`/api/uploads/${task.uploadId}`,{method:'DELETE'})}catch{/* stale cleanup retries later */}}}
async function retry(task:UploadTask){await abortRemote(task);task.status='queued';task.error='';task.uploadId=undefined;task.requests=[];task.cancelled=false;pumpQueue()}
function clearFinished(){for(let i=tasks.length-1;i>=0;i--)if(['done','cancelled'].includes(tasks[i].status))tasks.splice(i,1);window.clearTimeout(autoClearTimer)}
// 全部上传结束后自动收起上传面板（失败项保留，供重试）。
let autoClearTimer=0
function scheduleAutoClear(){window.clearTimeout(autoClearTimer);if(unfinished.value.length)return;autoClearTimer=window.setTimeout(clearFinished,4000)}
function formatSize(bytes:number){if(bytes===0)return'0 B';const units=['B','KB','MB','GB','TB'];const i=Math.min(Math.floor(Math.log(bytes)/Math.log(1024)),4);return`${(bytes/1024**i).toFixed(i?1:0)} ${units[i]}`}
function formatDate(value:string){const d=new Date(value);return Number.isNaN(d.valueOf())?'—':new Intl.DateTimeFormat('zh-CN',{month:'short',day:'numeric',hour:'2-digit',minute:'2-digit'}).format(d)}

onMounted(()=>{const saved=localStorage.getItem('cloud-view-mode');if(saved==='list'||saved==='grid')viewMode.value=saved;window.addEventListener('keydown',handlePreviewKey);window.addEventListener('popstate',handlePopState);checkSession()})
onBeforeUnmount(()=>{window.removeEventListener('keydown',handlePreviewKey);window.removeEventListener('popstate',handlePopState)})
</script>

<template>
  <div v-if="checking" class="splash"><div class="brand-mark"><img src="/logo.png" alt=""></div><div class="spinner"></div></div>
  <main v-else-if="!user" class="login-page">
    <section class="login-visual"><div class="glow glow-a"></div><div class="glow glow-b"></div><div class="visual-copy"><span class="eyebrow">PRIVATE · DIRECT · YOURS</span><h1>你的文件，<br>安静地待在云上。</h1><p>轻量、自托管，文件按内容块直传你的 S3。</p></div><div class="cloud-card"><span>☁</span><div><strong>Seafile 式块存储</strong><small>内容寻址 · 跨文件去重</small></div></div></section>
    <section class="login-panel"><form class="login-form" @submit.prevent="submitLogin"><div class="logo"><span class="brand-mark small"><img src="/logo.png" alt=""></span><span>Cloud</span></div><div><p class="eyebrow dark">WELCOME BACK</p><h2>登录私人空间</h2><p class="muted">首次启动的随机凭据可在容器日志中查看</p></div><label>用户名<input v-model="login.username" autocomplete="username" maxlength="128" required></label><label>密码<input v-model="login.password" type="password" autocomplete="current-password" maxlength="1024" required></label><p v-if="login.notice" class="form-success">{{ login.notice }}</p><p v-if="login.error" class="form-error">{{ login.error }}</p><button class="primary wide" :disabled="login.busy">{{ login.busy ? '正在验证…' : '进入我的网盘' }}</button></form></section>
  </main>

  <div v-else class="app-shell" @dragover.prevent="dragActive=true" @dragleave.self="dragActive=false" @drop.prevent="onDrop">
    <header class="topbar"><button class="logo brand-button" title="回到我的文件" @click="openFolder(ROOT)"><span class="brand-mark small"><img src="/logo.png" alt=""></span><span>Cloud</span></button><div class="top-actions"><span class="connection"><i></i>S3 块直传</span><button class="account-button" title="打开账户设置" @click="showAccount"><span class="avatar-badge"><img v-if="hasAvatar" :src="avatarURL" alt="个人头像" @error="hasAvatar=false"><template v-else>{{ user.slice(0,1).toUpperCase() }}</template></span><span class="account-copy"><b>{{ user }}</b><small>账户设置</small></span></button><button class="top-logout" @click="logout">退出</button></div></header>
    <aside class="sidebar"><button class="nav active" title="回到我的文件" @click="openFolder(ROOT)"><span>▰</span>我的文件</button><div class="sidebar-note"><span class="sidebar-label">存储空间</span><div class="storage-total"><strong>{{ formatSize(storageStats.total_bytes) }}</strong><small>逻辑占用</small></div><div class="storage-bar"><i></i></div><div class="storage-meta"><span class="storage-stat"><b>{{ storageStats.file_count }}</b><small>个文件</small></span><span class="storage-stat"><b>去重</b><small>SHA-256 块</small></span></div><p>内容以内容寻址块保存在 S3，重复内容只存一份。</p></div></aside>
    <section class="content" @click="clearSelectionFromBlank">
      <div class="content-head"><div><nav class="breadcrumbs" aria-label="路径"><button v-for="crumb in breadcrumbs" :key="crumb.id" @click="openFolder(crumb.id)">{{ crumb.name || '我的文件' }}<span>/</span></button></nav><div class="title-row"><h1>{{ current?.name || '我的文件' }}</h1><button v-if="currentId!==ROOT&&current?.parent_id" class="up-button" title="返回上一级" aria-label="返回上一级" @click="goUp"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 19V6m0 0-5 5m5-5 5 5"/></svg></button></div><p>{{ items.length }} 个项目 · {{ pathTitle }}</p><div class="mobile-stats"><span>总占用 <b>{{ formatSize(storageStats.total_bytes) }}</b></span><span>{{ storageStats.file_count }} 个文件</span></div></div><div class="actions"><div class="view-switch" role="group" aria-label="文件显示方式"><button :class="{active:viewMode==='list'}" title="列表视图" aria-label="列表视图" @click="setViewMode('list')"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M8 6h12M8 12h12M8 18h12M4 6h.01M4 12h.01M4 18h.01"/></svg></button><button :class="{active:viewMode==='grid'}" title="大图标视图" aria-label="大图标视图" @click="setViewMode('grid')"><svg viewBox="0 0 24 24" aria-hidden="true"><rect x="4" y="4" width="6" height="6" rx="1"/><rect x="14" y="4" width="6" height="6" rx="1"/><rect x="4" y="14" width="6" height="6" rx="1"/><rect x="14" y="14" width="6" height="6" rx="1"/></svg></button></div><button class="secondary" @click="newDocument">＋ 新建文档</button><button class="secondary" @click="createFolder">＋ 新建文件夹</button><button class="primary" @click="chooseFiles">↑ 上传文件</button><input ref="fileInput" hidden type="file" multiple @change="e=>{const el=e.target as HTMLInputElement;if(el.files)acceptFiles(el.files);el.value=''}"></div></div>
      <div v-if="viewMode==='grid'&&selected&&!modal" class="selection-toolbar" role="toolbar" aria-label="所选项目操作">
        <button class="selection-close" title="取消选择" aria-label="取消选择" @click="selected=null">×</button><span class="selection-summary"><b>1 项</b><small>已选择 {{ formatSize(selected.size) }}</small></span>
        <div class="selection-actions">
          <button v-if="selected.kind==='directory'||isEditable(selected)||isMedia(selected)||isBook(selected)" @click="openItem(selected)"><svg viewBox="0 0 24 24" aria-hidden="true"><path v-if="selected.kind==='directory'" d="M3 7h7l2 2h9v9H3z"/><path v-else-if="isEditable(selected)&&!isBook(selected)" d="m4 16-.8 4 4-.8L18.5 7.9l-3.2-3.2L4 16Z"/><path v-else-if="isBook(selected)" d="M12 5c-1.7-1.4-4.2-2-8-2v14c3.8 0 6.3.6 8 2 1.7-1.4 4.2-2 8-2V3c-3.8 0-6.3.6-8 2Zm0 0v14"/><path v-else-if="isImage(selected)" d="M2.5 12s3.5-6 9.5-6 9.5 6 9.5 6-3.5 6-9.5 6-9.5-6-9.5-6Z"/><path v-else d="M8 5v14l11-7Z"/></svg><span>{{ selected.kind==='directory'?'打开':isBook(selected)?'阅读':isEditable(selected)?'编辑文本':isImage(selected)?'预览':'播放' }}</span></button>
          <button v-if="selected.kind==='file'" @click="download(selected)"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3v12m0 0 4-4m-4 4-4-4M5 20h14"/></svg><span>下载</span></button>
          <button v-if="selected.kind==='file'" @click="showShare(selected)"><svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="18" cy="5" r="2.5"/><circle cx="6" cy="12" r="2.5"/><circle cx="18" cy="19" r="2.5"/><path d="m8.2 10.8 7.6-4.4M8.2 13.2l7.6 4.4"/></svg><span>分享</span></button>
          <button @click="showRename(selected)"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 5h14M12 5v14M9 19h6"/></svg><span>重命名</span></button>
          <button @click="showMove(selected)"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 12h14m-5-5 5 5-5 5"/></svg><span>移动</span></button>
          <button class="danger" @click="removeItem(selected)"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 7h16M9 7V4h6v3m3 0-1 13H7L6 7m4 4v5m4-5v5"/></svg><span>删除</span></button>
        </div>
      </div>
      <div v-if="loading" class="state"><div class="spinner"></div><p>正在读取文件…</p></div>
      <div v-else-if="!items.length" class="state empty"><div class="empty-icon">⌁</div><h3>这里还是空的</h3><p>拖放文件到这里，或新建一篇文档。</p><div class="empty-actions"><button class="secondary" @click="newDocument">新建文档</button><button class="primary" @click="chooseFiles">上传文件</button></div></div>
      <div v-else-if="viewMode==='list'" class="file-table">
        <div class="table-head"><span>名称</span><span>大小</span><span>修改时间</span><span>操作</span></div>
        <div v-for="item in items" :key="item.id" class="file-row" :class="{mutedrow:item.status!=='ready'}" @dblclick="openItem(item)">
          <div class="file-name"><button class="file-icon" :class="{directory:item.kind==='directory',image:isImage(item),document:isEditable(item),video:isVideo(item),audio:isAudio(item)}" :title="item.kind==='directory'?'打开文件夹':isEditable(item)?'编辑文档':isImage(item)?'预览图片':isVideo(item)?'播放视频':isAudio(item)?'播放音频':'文件'" @click="openItem(item)"><span v-if="item.kind==='directory'" class="folder-glyph">▰</span><img v-else-if="isImage(item)" :src="previewURL(item)" :alt="item.name" loading="lazy" @error="hideBrokenImage"><span v-else-if="isEditable(item)">▤</span><span v-else-if="isVideo(item)">▶</span><span v-else-if="isAudio(item)">♫</span><span v-else>◇</span></button><div><strong>{{ item.name }}</strong><small v-if="item.status!=='ready'">{{ item.status }}</small><small v-else>{{ item.kind==='directory'?'文件夹':item.mime_type||'文件' }}</small></div></div>
          <span>{{ item.kind==='directory'?'—':formatSize(item.size) }}</span><span>{{ formatDate(item.updated_at) }}</span>
          <div class="row-actions">
            <button v-if="isEditable(item)" title="编辑" aria-label="编辑" @click="openEditor(item)"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="m4 16-.8 4 4-.8L18.5 7.9l-3.2-3.2L4 16Z"/><path d="m13.8 6.2 3.2 3.2"/></svg></button>
            <button v-if="isMedia(item)" :title="isImage(item)?'预览':'播放'" :aria-label="isImage(item)?'预览':'播放'" @click="showPreview(item)"><svg viewBox="0 0 24 24" aria-hidden="true"><template v-if="isImage(item)"><path d="M2.5 12s3.5-6 9.5-6 9.5 6 9.5 6-3.5 6-9.5 6-9.5-6-9.5-6Z"/><circle cx="12" cy="12" r="2.6"/></template><path v-else d="M8 5v14l11-7Z"/></svg></button>
            <button v-if="isBook(item)" title="阅读" aria-label="阅读" @click="openReader(item)"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 5c-1.7-1.4-4.2-2-8-2v14c3.8 0 6.3.6 8 2 1.7-1.4 4.2-2 8-2V3c-3.8 0-6.3.6-8 2Zm0 0v14"/></svg></button>
            <button v-if="item.kind==='file'" title="下载" aria-label="下载" @click="download(item)"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3v12m0 0 4-4m-4 4-4-4M5 20h14"/></svg></button>
            <button v-if="item.kind==='file'" title="分享" aria-label="分享" @click="showShare(item)"><svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="18" cy="5" r="2.5"/><circle cx="6" cy="12" r="2.5"/><circle cx="18" cy="19" r="2.5"/><path d="m8.2 10.8 7.6-4.4M8.2 13.2l7.6 4.4"/></svg></button>
            <button title="重命名" aria-label="重命名" @click="showRename(item)"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 5h14M12 5v14M9 19h6"/></svg></button>
            <button title="移动" aria-label="移动" @click="showMove(item)"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 12h14m-5-5 5 5-5 5"/></svg></button>
            <button title="删除" aria-label="删除" class="danger" @click="removeItem(item)"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 7h16M9 7V4h6v3m3 0-1 13H7L6 7m4 4v5m4-5v5"/></svg></button>
          </div>
        </div>
      </div>
      <div v-else class="file-grid">
        <article v-for="item in items" :key="item.id" class="file-card" :class="{mutedrow:item.status!=='ready',selected:selected?.id===item.id}" @dblclick="openItem(item)">
          <button class="card-select" :class="{active:selected?.id===item.id}" :title="selected?.id===item.id?'取消选择':'选择项目'" :aria-label="selected?.id===item.id?'取消选择':'选择项目'" @click.stop="toggleSelection(item)"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="m5 12 4 4L19 6"/></svg></button>
          <button class="card-preview" :title="item.kind==='directory'?'打开文件夹':isEditable(item)?'编辑文档':isImage(item)?'预览图片':isVideo(item)?'播放视频':isAudio(item)?'播放音频':'文件'" @click="openItem(item)"><img v-if="isImage(item)" :src="previewURL(item)" :alt="item.name" loading="lazy" @error="hideBrokenImage"><span v-else-if="item.kind==='directory'" class="large-folder">▰</span><span v-else-if="isEditable(item)" class="large-document">▤</span><span v-else-if="isVideo(item)" class="large-video">▶</span><span v-else-if="isAudio(item)" class="large-audio">♫</span><span v-else class="large-file">◇</span></button>
          <div class="card-info"><strong :title="item.name">{{ item.name }}</strong><small>{{ item.kind==='directory'?'文件夹':formatSize(item.size) }} · {{ formatDate(item.updated_at) }}</small></div>
        </article>
      </div>
    </section>

    <div v-if="dragActive" class="drop-zone"><div><span>↓</span><h2>释放以上传到 {{ current?.name || '我的文件' }}</h2><p>文件将按内容块直传 S3，重复内容自动去重</p></div></div>
    <section v-if="tasks.length" class="upload-panel"><header><div><strong>上传</strong><span v-if="unfinished.length">{{ unfinished.length }} 项进行中</span></div><button @click="clearFinished">清除已完成</button></header><div class="task-list"><article v-for="task in tasks" :key="task.id"><div class="task-top"><span class="task-icon">↑</span><div><strong>{{ task.file.name }}</strong><small>{{ formatSize(task.file.size) }} · {{ task.status==='queued'?'等待中':task.status==='uploading'?'正在上传':task.status==='done'?'已完成':task.status==='cancelled'?'已取消':task.error }}</small></div><b>{{ task.progress }}%</b><button v-if="task.status==='queued'||task.status==='uploading'" @click="cancelUpload(task)">×</button><button v-else-if="task.status==='failed'" @click="retry(task)">重试</button></div><div class="progress"><i :class="task.status" :style="{width:`${task.progress}%`}"></i></div></article></div></section>

    <div v-if="modal" class="modal-backdrop" :class="{previewing:modal==='preview',editing:modal==='editor',reading:modal==='reader'}" @click.self="closeBackdrop">
      <section v-if="modal==='rename'" class="modal"><header><div><p class="eyebrow dark">EDIT</p><h2>重命名</h2></div><button @click="closeModal">×</button></header><label>新名称<input v-model="renameValue" maxlength="1024" @keyup.enter="saveRename"></label><footer><button class="secondary" @click="closeModal">取消</button><button class="primary" :disabled="modalBusy" @click="saveRename">保存</button></footer></section>
      <section v-else-if="modal==='move'" class="modal folder-modal"><header><div><p class="eyebrow dark">MOVE</p><h2>移动「{{ selected?.name }}」</h2></div><button @click="closeModal">×</button></header><div v-if="modalBusy" class="state small"><div class="spinner"></div></div><div v-else class="folder-list"><button v-for="folder in folders" :key="folder.id" :style="{paddingLeft:`${18+folder.depth*22}px`}" @click="moveTo(folder.id)"><span>▰</span>{{ folder.name }}</button></div></section>
      <section v-else-if="modal==='account'" class="modal account-modal"><header><div><p class="eyebrow dark">PROFILE & SECURITY</p><h2>账户设置</h2></div><button @click="closeModal">×</button></header><div class="account-layout"><section class="avatar-settings"><div class="avatar-large"><img v-if="hasAvatar" :src="avatarURL" alt="个人头像"><span v-else>{{ user.slice(0,1).toUpperCase() }}</span></div><h3>个人头像</h3><p>支持 JPG、PNG、GIF 和 WebP，最大 2 MiB。</p><div class="avatar-actions"><button type="button" class="secondary" :disabled="avatar.busy" @click="chooseAvatar">{{ avatar.busy?'处理中…':hasAvatar?'更换头像':'上传头像' }}</button><button v-if="hasAvatar" type="button" class="danger-text" :disabled="avatar.busy" @click="removeAvatar">移除</button></div><input ref="avatarInput" hidden type="file" accept="image/jpeg,image/png,image/gif,image/webp" @change="e=>{const el=e.target as HTMLInputElement;if(el.files?.[0])uploadAvatar(el.files[0]);el.value=''}"><p v-if="avatar.error" class="form-error">{{ avatar.error }}</p></section><form @submit.prevent="saveAccount"><div><h3>登录凭据</h3><p class="modal-hint">修改后会退出所有已登录设备，请使用新凭据重新登录。</p></div><label>管理员用户名<input v-model="account.username" autocomplete="username" maxlength="128" required></label><label>当前密码<input v-model="account.currentPassword" type="password" autocomplete="current-password" maxlength="1024" required></label><div class="account-passwords"><label>新密码<input v-model="account.password" type="password" autocomplete="new-password" minlength="12" maxlength="1024" required></label><label>确认新密码<input v-model="account.confirmPassword" type="password" autocomplete="new-password" minlength="12" maxlength="1024" required></label></div><p v-if="account.error" class="form-error">{{ account.error }}</p><footer><button type="button" class="secondary" @click="closeModal">取消</button><button class="primary" :disabled="modalBusy">{{ modalBusy?'正在保存…':'更新并退出' }}</button></footer></form></div></section>
      <section v-else-if="modal==='editor'" class="document-editor">
        <header class="editor-header">
          <div class="editor-title"><span>▤</span><div><input v-if="editor.isNew" v-model="editor.name" aria-label="文档文件名" maxlength="1024"><strong v-else :title="editor.name">{{ editor.name }}</strong><small>{{ editor.isNew?'保存在当前文件夹':'文本编辑器' }}</small></div></div>
          <div v-if="editorIsMarkdown" class="editor-tabs" role="group" aria-label="编辑器视图"><button :class="{active:editor.mode==='edit'}" @click="editor.mode='edit'">编辑</button><button :class="{active:editor.mode==='split'}" @click="editor.mode='split'">分栏</button><button :class="{active:editor.mode==='preview'}" @click="editor.mode='preview'">预览</button></div>
          <div class="editor-actions"><span v-if="editor.isNew||editorDirty" class="unsaved-dot">未保存</span><button class="primary" :disabled="editor.busy||(!editor.isNew&&!editorDirty)" @click="saveDocument">{{ editor.busy?'保存中…':'保存' }}</button><button class="editor-close" aria-label="关闭编辑器" @click="closeEditor">×</button></div>
        </header>
        <div v-if="editor.busy&&!editor.content" class="state editor-loading"><div class="spinner"></div><p>正在打开文档…</p></div>
        <div v-else class="editor-workspace" :class="[`mode-${editor.mode}`,{markdown:editorIsMarkdown}]">
          <textarea v-if="editor.mode!=='preview'" v-model="editor.content" autofocus spellcheck="false" aria-label="文档内容" @keydown.ctrl.s.prevent="saveDocument" @keydown.meta.s.prevent="saveDocument"></textarea>
          <article v-if="editorIsMarkdown&&editor.mode!=='edit'" class="markdown-preview" v-html="renderedMarkdown"></article>
        </div>
        <footer class="editor-status"><span>{{ editorBytes.toLocaleString() }} 字节 · UTF-8 · 最大 1 MiB</span><span v-if="editor.error" class="form-error">{{ editor.error }}</span><span v-else>Ctrl / ⌘ + S 保存</span></footer>
      </section>
      <section v-else-if="modal==='share'" class="modal share-modal">
        <header><div class="share-title"><span>↗</span><div><h2>分享文件</h2><p :title="selected?.name">{{ selected?.name }}</p></div></div><button @click="closeModal">×</button></header>
        <div v-if="share.busy" class="state small"><div class="spinner"></div><p>正在准备分享…</p></div>
        <template v-else-if="share.active">
          <p class="share-description">任何拿到链接的人都能直接读取该文件。重新生成或停止分享后，旧链接立即失效。</p>
          <div class="share-link"><input :value="share.url" aria-label="分享链接" readonly @focus="($event.target as HTMLInputElement).select()"><button type="button" class="primary" @click="copyShare">{{ share.copied?'已复制':'复制链接' }}</button></div>
          <p v-if="share.createdAt" class="share-created">公开链接 · 创建于 {{ formatDate(share.createdAt) }}</p>
          <p v-if="share.error" class="form-error">{{ share.error }}</p>
          <footer class="share-footer"><button class="danger-text" :disabled="share.busy" @click="revokeShare">停止分享</button><button class="secondary" :disabled="share.busy" @click="createShare(true)">重新生成链接</button></footer>
        </template>
        <template v-else><p class="share-description">创建后，无需登录即可通过链接读取这个文件。你可以随时重新生成或停止分享。</p><button class="primary share-create" :disabled="share.busy" @click="createShare(false)">创建公开链接</button><p v-if="share.error" class="form-error">{{ share.error }}</p></template>
      </section>
      <section v-else class="preview-modal" @click.self="closeModal">
        <header class="preview-bar"><div><strong>{{ selected?.name }}</strong><small v-if="selected">{{ formatSize(selected.size) }} · {{ selected.mime_type||'媒体文件' }}</small></div><span v-if="galleryIndex>=0" class="preview-count">{{ galleryIndex+1 }} / {{ galleryItems.length }}</span><button aria-label="关闭预览" @click="closeModal">×</button></header>
        <div ref="stageEl" class="preview-stage" :class="{swipeable:stageSwipeable}" @click="onStageClick" @pointerdown="onStagePointerDown" @pointermove="onStagePointerMove" @pointerup="onStagePointerEnd" @pointercancel="onStagePointerEnd">
          <button v-if="hasGalleryNavigation" class="preview-nav preview-prev" aria-label="上一项" @click.stop="changePreview(-1)"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="m14.5 6-6 6 6 6"/></svg></button>
          <img v-if="selected&&isImage(selected)" :key="selected.id" :src="previewURL(selected)" :alt="selected.name" :style="swipeStyle">
          <video v-else-if="selected&&isVideo(selected)" :key="selected.id" :src="previewURL(selected)" controls autoplay playsinline preload="metadata">你的浏览器不支持这个视频格式。</video>
          <div v-else-if="selected&&isAudio(selected)" class="audio-player-card"><span>♫</span><strong>{{ selected.name }}</strong><small>{{ formatSize(selected.size) }}</small><audio :key="selected.id" :src="previewURL(selected)" controls autoplay preload="metadata">你的浏览器不支持这个音频格式。</audio></div>
          <button v-if="hasGalleryNavigation" class="preview-nav preview-next" aria-label="下一项" @click.stop="changePreview(1)"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="m9.5 6 6 6-6 6"/></svg></button>
        </div>
        <footer class="preview-toolbar"><button class="preview-download" @click="selected&&download(selected)"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3v12m0 0 4-4m-4 4-4-4M5 20h14"/></svg>{{ previewDownloadLabel }}</button></footer>
      </section>
    </div>
    <ReaderView v-if="modal==='reader'&&readerFile" :file="readerFile" @close="closeModal" />
    <div v-if="toast.text" class="toast" :class="toast.kind">{{ toast.text }}</div>
  </div>
</template>
