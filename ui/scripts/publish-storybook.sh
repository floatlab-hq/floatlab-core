#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
repository=floatlab-hq/floatlab-ui
gh auth status
BASE_PATH=/floatlab-ui/ pnpm storybook:build
test -s storybook-static/index.html
temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT
git -C "$temporary" init -q
git -C "$temporary" remote add origin "https://github.com/$repository.git"
git -C "$temporary" config credential.helper '!gh auth git-credential'
git -C "$temporary" config user.name "$(gh api user --jq .login)"
git -C "$temporary" config user.email "$(gh api user --jq .id)+$(gh api user --jq .login)@users.noreply.github.com"
if git -C "$temporary" ls-remote --exit-code origin refs/heads/gh-pages >/dev/null; then
  git -C "$temporary" fetch -q origin gh-pages
  git -C "$temporary" checkout -q -b gh-pages FETCH_HEAD
  git -C "$temporary" rm -qr --ignore-unmatch .
else
  git -C "$temporary" checkout -q --orphan gh-pages
fi
cp -a storybook-static/. "$temporary/"
touch "$temporary/.nojekyll"
git -C "$temporary" add .
if ! git -C "$temporary" diff --cached --quiet; then
  git -C "$temporary" commit -qm "Publish Storybook from floatlab-core $(git rev-parse --short HEAD)"
  git -C "$temporary" push origin gh-pages
fi
# The old workflow builds the retired source and would overwrite this gallery.
gh workflow disable pages.yml --repo "$repository"
gh api --method PUT "repos/$repository/pages" --input - <<'JSON'
{"build_type":"legacy","source":{"branch":"gh-pages","path":"/"}}
JSON
gh api --method POST "repos/$repository/pages/builds"
gh api "repos/$repository/pages" --jq .html_url
