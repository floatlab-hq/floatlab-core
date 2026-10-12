// @vitest-environment node
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, rmSync, copyFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { execFileSync } from 'node:child_process'
import { describe, expect, it } from 'vitest'

describe('Local Storybook publishing', () => {
  it.each(['existing', 'new'])('publishes a %s branch without force or source files', branch => {
    const directory = mkdtempSync(join(tmpdir(), 'floatlab-publish-test-'))
    try {
      mkdirSync(join(directory, 'scripts'))
      mkdirSync(join(directory, 'bin'))
      copyFileSync('scripts/publish-storybook.sh', join(directory, 'scripts/publish-storybook.sh'))
      const log = join(directory, 'commands')
      const commands = {
        pnpm: 'mkdir -p storybook-static; echo gallery > storybook-static/index.html; echo "$BASE_PATH" > "$TEST_BASE"',
        gh: 'echo "gh $*" >> "$TEST_LOG"; case "$*" in *".login"*) echo tester;; *".id"*) echo 123;; esac; if [ "$1" = api ] && [ "$2" = --method ]; then cat >> "$TEST_LOG"; fi',
        git: 'echo "git $*" >> "$TEST_LOG"; case "$*" in *"ls-remote"*) [ "$TEST_BRANCH" = existing ];; *"diff --cached"*) exit 1;; *"rev-parse"*) echo test-sha;; esac',
      }
      for (const [command, body] of Object.entries(commands)) writeFileSync(join(directory, 'bin', command), `#!/usr/bin/env bash\n${body}\n`, { mode: 0o755 })
      execFileSync('bash', [join(directory, 'scripts/publish-storybook.sh')], {
        env: { ...process.env, PATH: `${join(directory, 'bin')}:${process.env.PATH}`, TEST_LOG: log, TEST_BASE: join(directory, 'base'), TEST_BRANCH: branch },
      })
      const calls = readFileSync(log, 'utf8')
      expect(readFileSync(join(directory, 'base'), 'utf8').trim()).toBe('/floatlab-ui/')
      expect(calls).toContain('push origin gh-pages')
      expect(calls).not.toContain('--force')
      expect(calls).toContain('workflow disable pages.yml --repo floatlab-hq/floatlab-ui')
      expect(calls).toContain('"build_type":"legacy"')
      expect(calls).toContain(branch === 'existing' ? 'fetch -q origin gh-pages' : 'checkout -q --orphan gh-pages')
    } finally { rmSync(directory, { recursive: true, force: true }) }
  })
})
