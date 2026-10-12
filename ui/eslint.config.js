import js from '@eslint/js'
import eslintPluginVue from 'eslint-plugin-vue'
import tseslint from 'typescript-eslint'

export default tseslint.config(
  { ignores: ['dist', '.histoire', 'coverage', 'storybook-static', 'src/vendor/avatar-engine.js'] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  ...eslintPluginVue.configs['flat/recommended'],
  {
    rules: {
      'vue/html-self-closing': 'off',
      'vue/max-attributes-per-line': 'off',
      'vue/singleline-html-element-content-newline': 'off',
    },
    languageOptions: {
      globals: {
        HTMLElement: 'readonly',
        HTMLButtonElement: 'readonly',
        HTMLTextAreaElement: 'readonly',
        setTimeout: 'readonly',
        clearTimeout: 'readonly',
        setInterval: 'readonly',
        clearInterval: 'readonly',
        document: 'readonly',
        KeyboardEvent: 'readonly',
        structuredClone: 'readonly',
        window: 'readonly',
      },
    },
  },
  {
    files: ['**/*.vue'],
    languageOptions: {
      globals: { HTMLDialogElement: 'readonly' },
      parserOptions: { parser: tseslint.parser },
    },
  },
  {
    files: ['**/*.spec.ts'],
    languageOptions: { globals: { HTMLDialogElement: 'readonly' } },
  },
)
