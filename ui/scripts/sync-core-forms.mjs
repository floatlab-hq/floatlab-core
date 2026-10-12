import { readFile, writeFile } from 'node:fs/promises'
import { resolve, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import process from 'node:process'
import console from 'node:console'
import { parse } from 'yaml'
const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const api = resolve(process.argv[2] ?? resolve(root, '../api/openapi'))
const forms = {}
for (const [file, names] of [['nodes.yaml', ['NodeCreate', 'NodeUpdate']], ['config.yaml', ['AlertRuleCreate']], ['stacks.yaml', ['StackCreate']]]) {
  const doc = parse(await readFile(resolve(api, file), 'utf8'))
  const schemas = doc.components.schemas
  for (const name of names) {
    const schema = schemas[name]
    forms[name] = { name, fields: Object.entries(schema.properties).filter(([, property]) => !property.deprecated).map(([key, original]) => {
      const property = original.$ref ? { ...schemas[original.$ref.split('/').at(-1)], ...original } : original
      const type = key === 'compose_file' ? 'multiline' : property.enum ? 'enum' : ['array', 'object'].includes(property.type) ? 'json' : property.type === 'boolean' ? 'boolean' : 'string'
      return { key, label: key.replaceAll('_', ' '), type, required: schema.required?.includes(key) ?? false,
        ...(property.description && { description: property.description.trim() }),
        ...(property.enum && { options: property.enum.map(value => ({ value, label: value })) }),
        ...(property.default !== undefined && { default: property.default }) }
    }) }
  }
}
await writeFile(resolve(root, 'src/forms/core-forms.json'), JSON.stringify(forms, null, 2) + '\n')
console.log('Updated NodeCreate, NodeUpdate AlertRuleCreate and StackCreate from floatlab-core OpenAPI')
