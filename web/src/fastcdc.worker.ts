import { cutPoint } from './fastcdc'
import type { FastCDCConfig } from './fastcdc'

interface StartMessage { file:File; config:FastCDCConfig }
type ResultMessage=
  |{type:'block';block:{id:string;size:number;offset:number};hashed:number}
  |{type:'done'}
  |{type:'error';message:string}

interface WorkerScope {
  onmessage:((event:MessageEvent<StartMessage>)=>void)|null
  postMessage(message:ResultMessage):void
}

const scope=self as unknown as WorkerScope

function hex(data:ArrayBuffer):string {
  return Array.from(new Uint8Array(data),byte=>byte.toString(16).padStart(2,'0')).join('')
}

scope.onmessage=event=>{
  void chunkFile(event.data.file,event.data.config).catch(error=>{
    scope.postMessage({type:'error',message:error instanceof Error?error.message:'FastCDC 分块失败'})
  })
}

async function chunkFile(file:File,config:FastCDCConfig):Promise<void>{
  if(!Number.isSafeInteger(config.minSize)||!Number.isSafeInteger(config.avgSize)||!Number.isSafeInteger(config.maxSize)||config.minSize<1||config.minSize>config.avgSize||config.avgSize>config.maxSize){
    throw new Error('服务端返回了无效的 FastCDC 参数')
  }
  const buffer=new Uint8Array(config.maxSize)
  const readSize=Math.min(config.maxSize,8*1024*1024)
  let readOffset=0
  let chunkOffset=0
  let used=0

  const emit=async(final:boolean)=>{
    while(used===config.maxSize||(final&&used>0)){
      const size=cutPoint(buffer.subarray(0,used),config)
      if(size<1)throw new Error('FastCDC 没有产生有效边界')
      const chunk=buffer.slice(0,size)
      const id=hex(await crypto.subtle.digest('SHA-256',chunk))
      scope.postMessage({type:'block',block:{id,size,offset:chunkOffset},hashed:chunkOffset+size})
      buffer.copyWithin(0,size,used)
      used-=size
      chunkOffset+=size
      if(!final)break
    }
  }

  // Read each source byte once. The carry buffer retains at most maxSize-1
  // bytes between reads, avoiding the 4x reread amplification of repeatedly
  // slicing a max-size window at every average-size cut point.
  while(readOffset<file.size){
    const end=Math.min(readOffset+readSize,file.size)
    const input=new Uint8Array(await file.slice(readOffset,end).arrayBuffer())
    readOffset=end
    let cursor=0
    while(cursor<input.length){
      const copied=Math.min(buffer.length-used,input.length-cursor)
      buffer.set(input.subarray(cursor,cursor+copied),used)
      used+=copied
      cursor+=copied
      if(used===buffer.length)await emit(false)
    }
  }
  await emit(true)
  scope.postMessage({type:'done'})
}
