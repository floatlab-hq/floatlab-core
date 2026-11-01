import type { Meta, StoryObj } from '@storybook/vue3-vite'
import type { Node, Stack } from '@floatlab/client'
import BackupTargets from './BackupTargets.vue'
const meta = { title: '2. Pattern/BackupTargets', component: BackupTargets, args: { stacks: [], nodes: [], api: { listBackupTargets: async () => [], putBackupTarget: async () => { throw new Error('Preview only') }, removeBackupTarget: async () => ({}), triggerReplication: async () => ({}), listReplication: async () => [] } } } satisfies Meta<typeof BackupTargets>
export default meta
export const Empty: StoryObj<typeof meta> = {}
export const RestoreBackup: StoryObj<typeof meta> = { args: { stacks: [{id:'app', name:'demo', primary_node:'a'}] as Stack[], nodes: [{id:'a', name:'Alpha'}, {id:'b', name:'Beta'}] as Node[], api: {...meta.args.api, getBackupConfig: async () => 'name: demo\nservices:\n  app:\n    image: alpine\n    volumes: ["./data:/data"]\n', listBackups: async () => [{name:'fl-0J8F40', dataset:'floatlab/backups/a/floatlab/demo/data', node_id:'b', used_bytes:1024, created_at:'2026-11-01T00:00:00Z'}], restoreBackup: async () => ({state:'pending'})} } }
