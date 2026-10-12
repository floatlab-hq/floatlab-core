export function publicAsset(src: string, base = import.meta.env.BASE_URL): string {
  return src.startsWith('/') && !src.startsWith('//') ? `${base}${src.slice(1)}` : src
}
