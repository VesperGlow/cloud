export interface DriveFile { id:string; parent_id:string|null; name:string; kind:'file'|'directory'; size:number; mime_type?:string; etag?:string; status:'pending'|'ready'|'deleting'|'failed'; created_at:string; updated_at:string }

export interface ApiError extends Error { status?:number; data?:unknown }

export async function api<T>(path:string, init:RequestInit = {}):Promise<T>{
  const headers = new Headers(init.headers)
  if (init.body && !headers.has('Content-Type')) headers.set('Content-Type','application/json')
  const response = await fetch(path, { ...init, headers, credentials:'same-origin' })
  if (!response.ok) {
    let message = `请求失败 (${response.status})`
    let payload: unknown = null
    try { payload = await response.json(); message = (payload as {error?:{message?:string}}).error?.message || message } catch { /* ignore */ }
    const error = new Error(message) as ApiError; error.status=response.status; error.data=payload; throw error
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}
