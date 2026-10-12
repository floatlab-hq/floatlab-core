import { createApiClient } from '@floatlab/client'

export function browserApi(token?: string) {
  return createApiClient({ baseUrl: new URL('/api/v1', window.location.origin).href, token })
}
