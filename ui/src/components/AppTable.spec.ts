import { mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { describe, expect, it } from 'vitest'
import AppTable from './AppTable.vue'
import type { AppRecord, Operation } from '../types'

const app = (
  id: string,
  name: string,
  cpu: number,
  status: AppRecord['status'] = 'Running',
): AppRecord => ({
  id,
  name,
  icon: '◇',
  iconClass: '',
  status,
  address: `${name}.local`,
  cpu: { label: `${cpu}%`, percent: cpu },
  memory: { label: '1 GB', percent: cpu },
  storage: { label: '1 TB', percent: cpu },
  disk: { label: `${cpu}%`, percent: cpu },
  network: {
    up: { label: '1 MB', percent: cpu },
    down: { label: '1 MB', percent: cpu },
  },
})

const apps = [
  app('charlie', 'Charlie', 30),
  app('alpha', 'Alpha', 10, 'Failed'),
  app('bravo-1', 'Bravo', 20),
  app('bravo-2', 'Bravo', 20),
]

const AppTableRowStub = defineComponent({
  name: 'AppTableRow',
  props: { app: { type: Object, required: true } },
  emits: ['operation'],
  template: '<tr class="app-table-row" :data-app-id="app.id"></tr>',
})

const render = () =>
  mount(AppTable, {
    props: { apps },
    global: { stubs: { AppTableRow: AppTableRowStub } },
  })

const rowIds = (wrapper: ReturnType<typeof render>) =>
  wrapper.findAll('.app-table-row').map((row) => row.attributes('data-app-id')!)

describe('AppTable', () => {
  it('renders the source columns, headers, and alphabetically sorted rows', () => {
    const wrapper = render()

    expect(wrapper.findAll('col')).toHaveLength(8)
    expect(wrapper.findAll('th').map((header) => header.text())).toEqual([
      '4 Apps',
      'State',
      'Info',
      'CPU',
      'Mem',
      'Storage',
      'Disk IO',
      'Network',
    ])
    expect(rowIds(wrapper)).toEqual(['alpha', 'bravo-1', 'bravo-2', 'charlie'])
  })

  it('sorts numeric columns, toggles direction, and marks only the active header', async () => {
    const wrapper = render()
    const cpu = wrapper.findAll('th')[3]

    await cpu.get('button').trigger('click')
    expect(rowIds(wrapper)).toEqual(['alpha', 'bravo-1', 'bravo-2', 'charlie'])
    expect(cpu.attributes('aria-sort')).toBe('ascending')
    expect(wrapper.findAll('th[aria-sort]')).toHaveLength(1)

    await cpu.get('button').trigger('click')
    expect(rowIds(wrapper)).toEqual(['charlie', 'bravo-1', 'bravo-2', 'alpha'])
    expect(cpu.attributes('aria-sort')).toBe('descending')
  })

  it('keeps equal values in their original order without mutating the prop', async () => {
    const originalOrder = apps.map(({ id }) => id)
    const wrapper = render()

    await wrapper.findAll('th')[3].get('button').trigger('click')
    await wrapper.findAll('th')[3].get('button').trigger('click')

    expect(rowIds(wrapper).filter((id) => id.startsWith('bravo'))).toEqual(['bravo-1', 'bravo-2'])
    expect(apps.map(({ id }) => id)).toEqual(originalOrder)
  })

  it('forwards a row operation with its app id', () => {
    const wrapper = render()
    const firstRow = wrapper.findAllComponents(AppTableRowStub)[0]

    firstRow.vm.$emit('operation', 'restart' satisfies Operation)

    expect(wrapper.emitted('operation')).toEqual([['alpha', 'restart']])
  })
})
