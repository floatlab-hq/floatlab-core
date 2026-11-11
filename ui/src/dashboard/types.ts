export type Health = 'ready' | 'stopped' | 'standby' | 'degraded' | 'unknown'
export type AppState = 'Running' | 'Stopped' | import('@floatlab/client').Stack['state']
export interface HostItem {
  id: string
  name: string
  health: Health
  network?: string
  address?: string
  apps: number
  standby?: number
  backupAttention?: boolean
  cpu: string
  memory?: number
  disk?: number
}
export interface TransferStatus {
  label: string
  host?: string
  hostHealth?: string
  busy?: boolean
  tone?: 'default' | 'warning' | 'error'
  completedAt?: string
  progress?: number
  detail?: string
  count?: number
}
export interface DirectoryApp {
  id: string
  name: string
  logo: string
  state: AppState
  address: string
  host: string
  standby: string
  backup: string
  group: string
  actionPending?: boolean
  moveAvailable?: boolean
  runtimeMotion?: 'restart'
  hostId?: string
  hostHealth?: string
  ports?: string[]
  replication?: TransferStatus
  backups?: TransferStatus
  protectionAvailable?: boolean
  replica?: boolean
  hostSwitch?: 'triggerFailover' | 'restoreStack'
  tls?: boolean
}
export interface LabEvent {
  id: string
  tone: 'error' | 'info' | 'warning'
  time: string
  host: string
  source: string
  message: string
}
