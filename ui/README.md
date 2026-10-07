# FloatLab UI

Vue 3 and Vite frontend using the shared browser-safe FloatLab client.

```sh
pnpm install
pnpm dev
```

Vite launches at `http://localhost:5173` and proxies `/api` to `http://localhost:8080` by default. To validate the UI against another backend, create `.env.local` (ignored by Git):

```sh
VITE_API_PROXY_TARGET=http://192.0.2.10:8080
```

Then launch `pnpm dev` as normal. Use the typed client from `src/api/client.ts`:

```ts
import { api } from "./api/client";

const stacks = await api.listStacks();
```

`pnpm lint` runs the recommended JavaScript, TypeScript, and Vue rules. `pnpm test` exercises palette keyboard and modal interactions. `pnpm build` type-checks, lints, and creates `dist/`.
