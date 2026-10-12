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
