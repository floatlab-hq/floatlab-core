export function nodeCode(name: string): string {
  const characters = name.normalize('NFKD').replace(/[\u0300-\u036f]/g, '').toUpperCase().replace(/[^A-Z0-9]/g, '')
  if (!characters) return '—'
  const consonants = characters.replace(/[AEIOU0-9]/g, '')
  const prefix = (consonants + characters).slice(0, 2).padEnd(2, characters[0])
  return prefix + characters.at(-1)
}
