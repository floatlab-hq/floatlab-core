set -euo pipefail

arc_max_file=${FLOATLAB_ARC_MAX_FILE:-/sys/module/zfs/parameters/zfs_arc_max}
read -r configured < "$arc_max_file"
# Preserve explicit administrator tuning, including limits below our default.
if [[ "$configured" != 0 ]]; then
  exit 0
fi
total_kib=$(awk '$1 == "MemTotal:" && $3 == "kB" { print $2 }' "${FLOATLAB_MEMINFO_FILE:-/proc/meminfo}")
default_max=$(awk '$1 == "c_max" { print $3 }' "${FLOATLAB_ARC_STATS_FILE:-/proc/spl/kstat/zfs/arcstats}")
if [[ ! "$total_kib" =~ ^[0-9]+$ || ! "$default_max" =~ ^[0-9]+$ ]]; then
  echo "Cannot determine safe ZFS ARC memory budget" >&2
  exit 1
fi
total_bytes=$((total_kib * 1024))
# Leave 3 GiB for production and bounded update tests, retaining a 128 MiB ARC floor.
budget=$((total_bytes - 3 * 1024 * 1024 * 1024))
if (( budget < 128 * 1024 * 1024 )); then
  budget=$((128 * 1024 * 1024))
fi
if (( budget < default_max )); then
  printf '%s\n' "$budget" > "$arc_max_file"
  echo "FloatLab ZFS ARC capped at $budget bytes for $total_bytes bytes RAM"
fi
