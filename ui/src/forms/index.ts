import definitions from './core-forms.json'
import type { FormSchema } from './schema'
export const coreForms = definitions as Record<keyof typeof definitions, FormSchema>
export const hostPreferences: FormSchema = { name: 'Design preferences', fields: [
  { key: 'resource_alerts', label: 'Resource alerts', type: 'boolean' },
  { key: 'automatic_updates', label: 'Automatic updates', type: 'boolean' },
] }
