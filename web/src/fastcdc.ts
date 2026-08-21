export interface FastCDCConfig {
  minSize:number
  avgSize:number
  maxSize:number
}

function mix32(value:number):number {
  let x=(value+0x9e3779b9)>>>0
  x=(x^(x>>>16))>>>0
  x=Math.imul(x,0x85ebca6b)>>>0
  x=(x^(x>>>13))>>>0
  x=Math.imul(x,0xc2b2ae35)>>>0
  x=(x^(x>>>16))>>>0
  return x&0x7fffffff
}

const GEAR=Uint32Array.from({length:256},(_,index)=>mix32(index))

function bitMask(bits:number):number {
  return (2**bits-1)>>>0
}

// Mirrors internal/fastcdc exactly. Keep this small synchronous kernel in a
// Web Worker: scanning a multi-gigabyte file must never stall the Vue UI.
export function cutPoint(data:Uint8Array,config:FastCDCConfig):number {
  const limit=Math.min(data.length,config.maxSize)
  if(limit<=config.minSize)return limit
  const bits=Math.round(Math.log2(config.avgSize))
  const smallMask=bitMask(bits+1)
  const largeMask=bitMask(bits-1)
  let center=config.avgSize-config.minSize-Math.ceil(config.minSize/2)
  center=Math.max(config.minSize,Math.min(center,config.maxSize))
  let pattern=0
  let i=config.minSize
  const firstBarrier=Math.min(center,limit)
  for(;i<firstBarrier;i++){
    pattern=((pattern>>>1)+GEAR[data[i]])>>>0
    if((pattern&smallMask)===0)return i+1
  }
  for(;i<limit;i++){
    pattern=((pattern>>>1)+GEAR[data[i]])>>>0
    if((pattern&largeMask)===0)return i+1
  }
  return limit
}
