# FloatLab UI

Vue 3 and Vite frontend with a Fetch-based client generated from the management OpenAPI specification.

```sh
pnpm install
pnpm dev
```

Vite launches at `http://localhost:5173` and proxies `/api` to `http://localhost:8080` by default. To validate the UI against another backend, create `.env.local` (ignored by Git):

```sh
VITE_API_PROXY_TARGET=http://192.0.2.10:8080
```

Then launch `pnpm dev` as normal. Regenerate the API types after changing any OpenAPI YAML file:

```sh
pnpm generate:api
```

Use the typed client from `src/api/client.ts`:

```ts
import { api } from "./api/client";

const { data, error } = await api.GET("/nodes");
```

`pnpm lint` runs the recommended JavaScript, TypeScript, and Vue rules. `pnpm build` regenerates the binding, type-checks, lints, and creates `dist/`.
