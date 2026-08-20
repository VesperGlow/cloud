export function formatSize(bytes:number){
  if(bytes===0)return '0 B'
  const units=['B','KB','MB','GB','TB']
  const index=Math.min(Math.floor(Math.log(bytes)/Math.log(1024)),units.length-1)
  return `${(bytes/1024**index).toFixed(index?1:0)} ${units[index]}`
}

export function formatDate(value:string){
  const date=new Date(value)
  return Number.isNaN(date.valueOf())
    ?'—'
    :new Intl.DateTimeFormat('zh-CN',{month:'short',day:'numeric',hour:'2-digit',minute:'2-digit'}).format(date)
}
