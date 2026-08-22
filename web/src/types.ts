export interface UploadTask {
  id:string
  file:File
  progress:number
  status:'queued'|'uploading'|'done'|'failed'|'cancelled'
  error:string
  cancelled:boolean
  uploadId?:string
  requests:XMLHttpRequest[]
}

export interface FolderOption { id:string; name:string; depth:number }
export interface ShareResponse { active:boolean; url?:string; created_at?:string }
export interface ProfileResponse { username:string; has_avatar:boolean }
export interface StorageStats { total_bytes:number; file_count:number }
export interface TOTPStatusResponse { enabled:boolean; recovery_codes:number }
export interface TOTPSetupResponse { secret:string; uri:string; qr_data_url:string }
export interface TOTPRecoveryResponse { enabled:boolean; recovery_codes:string[] }
