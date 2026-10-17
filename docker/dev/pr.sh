#!/usr/bin/env bash
set -euo pipefail
cd /workspace
[ "$#" -ge 1 ] || { echo 'Usage: floatlab-dev-pr TITLE [gh pr create options...] (commit reviewed files first)' >&2; exit 2; }
title=$1
shift
gh auth status || gh auth login --hostname github.com --git-protocol https --web
[ -z "$(git status --porcelain)" ] || { echo 'Review and commit your changes before opening a PR.' >&2; exit 1; }
branch=$(git symbolic-ref --short HEAD)
case "$branch" in main|master) echo 'Create a development branch first.' >&2; exit 1;; esac
gh auth setup-git
git push --set-upstream origin "$branch"
gh pr create --draft --fill --title "$title" "$@"
