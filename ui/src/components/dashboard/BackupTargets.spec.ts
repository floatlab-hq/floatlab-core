import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { BackupApi, BackupRestoreApi, BackupTarget, Node, Stack } from '@floatlab/client'
import BackupTargets from './BackupTargets.vue'
enableAutoUnmount(afterEach)
const target: BackupTarget = { stack_id: 'app', dataset: 'floatlab/demo/data', node_id: 'b', source_node_id: 'a', schedule: 'h/24 d/28', local_policy: 'h/24', enabled: true, cleanup_pending: false }
function fixture() {
  const api: BackupApi = { listBackupTargets: vi.fn().mockResolvedValue([target]), listReplication: vi.fn().mockResolvedValue([{ id: 'job', dataset: target.dataset, dest_node_id: 'b', state: 'failed', error: 'Connection lost' }]), putBackupTarget: vi.fn().mockResolvedValue(target), removeBackupTarget: vi.fn().mockResolvedValue({}), triggerReplication: vi.fn().mockResolvedValue({}) }
  return { api, stacks: [{ id: 'app', name: 'demo', primary_node: 'a' }] as Stack[], nodes: [{ id: 'a', name: 'Alpha' }, { id: 'b', name: 'Beta' }] as Node[] }
}
describe('Backup destinations', () => {
  beforeEach(() => vi.stubGlobal('crypto', { getRandomValues: globalThis.crypto.getRandomValues.bind(globalThis.crypto) }))
  afterEach(() => vi.unstubAllGlobals())
  it('edits per-mount policy and resolves destination node names', async () => {
    const props = fixture(); const wrapper = mount(BackupTargets, { props })
    await wrapper.get('select').setValue('app'); await flushPromises()
    expect(wrapper.text()).toContain('Beta'); expect(wrapper.text()).toContain('Connection lost')
    expect(wrapper.text()).toContain('rolls back destination-only changes')
    await wrapper.findAll('input')[0]!.setValue(target.dataset)
    await wrapper.findAll('select')[1]!.setValue('b')
    await wrapper.findAll('button').find(item => item.text() === 'Save backup target')!.trigger('click'); await flushPromises()
    expect(props.api.putBackupTarget).toHaveBeenCalledWith('app', { dataset: target.dataset, node_id: 'b', schedule: 'h/24 d/28' }, expect.any(String))
  })
  it('triggers replication, removes a target and shows pending cleanup', async () => {
    const props = fixture(); const wrapper = mount(BackupTargets, { props })
    await wrapper.get('select').setValue('app'); await flushPromises()
    await wrapper.findAll('button').find(item => item.text() === 'Replicate now')!.trigger('click'); await flushPromises()
    expect(props.api.triggerReplication).toHaveBeenCalledWith('app', 'b', target.dataset, expect.any(String))
    vi.mocked(props.api.listBackupTargets).mockResolvedValue([{ ...target, enabled: false, cleanup_pending: true, error: 'Source offline' }])
    await wrapper.findAll('button').find(item => item.text() === 'Remove target')!.trigger('click'); await flushPromises()
    expect(props.api.removeBackupTarget).toHaveBeenCalledWith('app', target, expect.any(String))
    expect(wrapper.text()).toContain('Cleanup pending'); expect(wrapper.text()).toContain('Source offline')
  })
  it('reports request failure and supports a status retry', async () => {
    const props = fixture(); vi.mocked(props.api.listBackupTargets).mockRejectedValueOnce(new Error('Offline'))
    const wrapper = mount(BackupTargets, { props }); await wrapper.get('select').setValue('app'); await flushPromises()
    expect(wrapper.text()).toContain('Offline')
    vi.mocked(props.api.listBackupTargets).mockResolvedValue([])
    await wrapper.findAll('button').find(item => item.text() === 'Refresh backups')!.trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('No backup targets configured'); expect(wrapper.text()).not.toContain('Offline')
  })
  it('restores all writable mounts at a common snapshot with editable cached Compose', async () => {
    const props = fixture()
    const compose = 'name: demo\nservices:\n  app:\n    image: alpine\n    volumes: ["./data:/data", "cache:/cache", "/etc/localtime:/etc/localtime:ro"]\nvolumes:\n  cache: {}\n'
    const restoreApi: BackupRestoreApi = {
      getBackupConfig: vi.fn().mockResolvedValue(compose),
      listBackups: vi.fn().mockImplementation(async query => query.dataset.endsWith('/data') ? [{name: 'fl-common'}, {name: 'fl-data-only'}] : [{name: 'fl-common'}]),
      restoreBackup: vi.fn().mockResolvedValue({operation_id: 'restore'}),
    }
    const wrapper = mount(BackupTargets, {props: {...props, api: {...props.api, ...restoreApi}}})
    await wrapper.get('select').setValue('app'); await flushPromises()
    await wrapper.get('[aria-label="Restore backup node"]').setValue('b')
    await wrapper.findAll('button').find(item => item.text() === 'Load restore points')!.trigger('click'); await flushPromises()
    expect(restoreApi.getBackupConfig).toHaveBeenCalledWith('app')
    expect(restoreApi.listBackups).toHaveBeenCalledWith({node_id: 'b', source_node_id: 'a', dataset: 'floatlab/demo/cache'})
    expect(wrapper.get('[aria-label="Restore snapshot"]').text()).not.toContain('fl-data-only')
    await wrapper.get('[aria-label="Restore snapshot"]').setValue('fl-common')
    await wrapper.get('[aria-label="Restore destination node"]').setValue('b')
    await wrapper.get('[aria-label="Restored app name"]').setValue('restored')
    await wrapper.findAll('button').find(item => item.text() === 'Restore and start')!.trigger('click'); await flushPromises()
    expect(restoreApi.restoreBackup).toHaveBeenCalledWith({name:'restored', source_node_id:'a', backup_node_id:'b', destination_node_id:'b', compose_file:compose, snapshots:[{dataset:target.dataset,snapshot:'fl-common'},{dataset:'floatlab/demo/cache',snapshot:'fl-common'}]}, expect.any(String))
    expect(wrapper.text()).toContain('Restore and start queued')
    expect(wrapper.emitted('restored')).toHaveLength(1)
    await wrapper.get('[aria-label="Restore Compose"]').setValue(compose.replace('alpine', 'alpine:3.20'))
    expect(wrapper.get('[aria-label="Restore snapshot"]').findAll('option')).toHaveLength(1)
  })
  it('explains incomplete backups, supports supplied config and shows restore errors', async () => {
    const props = fixture()
    const restoreApi: BackupRestoreApi = {getBackupConfig:vi.fn(), listBackups:vi.fn().mockResolvedValue([]), restoreBackup:vi.fn().mockRejectedValue(new Error('Restore rejected'))}
    const wrapper = mount(BackupTargets, {props:{...props, api:{...props.api,...restoreApi}}})
    await wrapper.get('select').setValue('app'); await flushPromises()
    await wrapper.get('[aria-label="Restore backup node"]').setValue('b')
    await wrapper.get('[aria-label="Restore Compose"]').setValue('name: demo\nservices:\n  app:\n    image: alpine\n    volumes: ["./data:/data"]\n')
    const load = wrapper.findAll('button').find(item => item.text() === 'Load restore points')!
    await load.trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('No common snapshot covers every writable mount')
    expect(restoreApi.getBackupConfig).not.toHaveBeenCalled()
    vi.mocked(restoreApi.listBackups).mockResolvedValue([{name:'fl-common'}] as Awaited<ReturnType<BackupRestoreApi['listBackups']>>)
    await load.trigger('click'); await flushPromises()
    await wrapper.get('[aria-label="Restore snapshot"]').setValue('fl-common')
    await wrapper.get('[aria-label="Restore destination node"]').setValue('a')
    await wrapper.get('[aria-label="Restored app name"]').setValue('restored')
    await wrapper.findAll('button').find(item => item.text() === 'Restore and start')!.trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('Restore rejected')
    await wrapper.get('[aria-label="Restore Compose"]').setValue('name: demo\nservices: {}')
    await load.trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('backed-up writable mounts')
  })
})
