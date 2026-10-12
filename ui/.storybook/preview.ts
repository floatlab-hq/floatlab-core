import type { Preview } from '@storybook/vue3-vite'
import '../src/styles.css'
import '../src/themes/setup'
import '../src/stories/foundations.css'
import { applyTheme, themes } from '../src/themes'

const preview: Preview = {
  globalTypes: {
    theme: {
      description: 'Omarchy palette applied to every component and view',
      toolbar: {
        title: 'Theme', icon: 'paintbrush', dynamicTitle: true,
        items: Object.values(themes).map((theme) => ({ value: theme.id, title: theme.name })),
      },
    },
  },
  initialGlobals: { theme: 'everforest' },
  decorators: [(story, context) => {
    applyTheme(context.globals.theme as string)
    return { components: { story }, template: '<div class="storybook-canvas"><story /></div>' }
  }],
  parameters: {
    layout: 'padded',
    backgrounds: { disable: true },
    options: { storySort: { order: ['1. Primitive', '2. Pattern', '3. Layout'] } },
  },
}

export default preview
