export interface FieldSchema {
  key: string
  label: string
  type: 'string' | 'boolean' | 'enum' | 'json' | 'multiline'
  description?: string
  required?: boolean
  options?: { value: string; label: string }[]
  default?: string | boolean
}
export interface FormSchema { name: string; fields: FieldSchema[] }
export type FormValues = Record<string, string | boolean>
