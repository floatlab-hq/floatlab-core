#!/bin/sh
set -eu

# Both ends use this exact SQLite release and protocol implementation.
destination=${1:?usage: build-sqlite-tools.sh OUTPUT_DIRECTORY}
build_dir=$(mktemp -d)
trap 'rm -rf "$build_dir"' EXIT HUP INT TERM
cd "$build_dir"
curl -fsSL https://sqlite.org/2025/sqlite-amalgamation-3500400.zip -o amalgamation.zip
curl -fsSL https://raw.githubusercontent.com/sqlite/sqlite/version-3.50.4/tool/sqlite3_rsync.c -o sqlite3_rsync.c
curl -fsSL https://raw.githubusercontent.com/sqlite/sqlite/version-3.50.4/ext/misc/sha1.c -o sha1.c
sha256sum -c <<'CHECKSUMS'
1d3049dd0f830a025a53105fc79fd2ab9431aea99e137809d064d8ee8356b032  amalgamation.zip
4ac00cc92c118d661386893ee3f15f0a205a219544b90cf89009fc844bff7bba  sqlite3_rsync.c
a598b927e504a34faba57e52702f0c9285d192c93f53c0543cfdaf82602a39c2  sha1.c
CHECKSUMS
unzip -q amalgamation.zip
mkdir -p "$destination"
cc -O2 -static -DSQLITE_CORE -DSQLITE_ENABLE_DBPAGE_VTAB -DSQLITE_OMIT_LOAD_EXTENSION \
  -Isqlite-amalgamation-3500400 sqlite3_rsync.c sha1.c \
  sqlite-amalgamation-3500400/sqlite3.c -lpthread -lm -o "$destination/sqlite3_rsync"
cc -O2 -static -DSQLITE_OMIT_LOAD_EXTENSION \
  sqlite-amalgamation-3500400/shell.c sqlite-amalgamation-3500400/sqlite3.c \
  -lpthread -lm -o "$destination/sqlite3"
"$destination/sqlite3_rsync" --version
"$destination/sqlite3" :memory: 'SELECT sqlite_version();'
