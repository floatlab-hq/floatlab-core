import { createApiClient } from "@floatlab/client";

// Browser authentication remains owned by the application session/cookie.
export const api = createApiClient({ baseUrl: "/api/v1" });
