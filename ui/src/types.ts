export type Operation = 'start' | 'restart' | 'stop'
export type Status = 'Starting' | 'Failed' | 'Restarting' | 'Running'
export type SortKey = 'name' | 'status' | 'info' | 'cpu' | 'memory' | 'storage' | 'disk' | 'network'

export interface ResourceValue {
  label: string
  percent: number
}

export interface NetworkValue {
  up: ResourceValue
  down: ResourceValue
}

export interface AppRecord {
  id: string
  name: string
  icon: string
  iconClass: string
  status: Status
  pendingOperation?: Operation
  links?: string[]
  address?: string
  notice?: string
  noticeTone?: 'error' | 'warning'
  cpu: ResourceValue
  memory: ResourceValue
  storage: ResourceValue
  disk: ResourceValue
  network: NetworkValue
}

export interface Metric {
  id: 'cpu' | 'memory' | 'storage' | 'disk'
  value: string
  detail: string
  percent: number
  history: number[]
}

export interface Server {
  name: string
  temperature: string
  uptime: string
  metrics: Metric[]
  network: NetworkValue
}

export interface Command {
  id: string
  label: string
  shortcut: string
}
