set -euo pipefail
shopt -s nullglob

media="$1"
runtime="$2"
answers_device="${3:-/dev/disk/by-label/FLOATLAB_ANSWERS}"
install -d -m 0700 "$runtime"
rm -f "$runtime/answers.toml"
files=()
mounts=()
cleanup() {
  for target in "${mounts[@]}"; do umount "$target"; done
}
trap cleanup EXIT

collect() {
  local directory="$1" file
  for file in "$directory"/floatlab-*.toml; do
    if [[ -f "$file" && ! -L "$file" ]]; then files+=("$file"); fi
  done
}
collect "$media"

# A written ISO is read-only; also inspect other partitions on that same USB.
source=$(findmnt -n -o SOURCE --target "$media") || exit 2
source=$(readlink -f "$source") || exit 2
parent=$(lsblk -dn -o PKNAME "$source") || exit 2
if [[ -z "$parent" && "$(lsblk -dn -o TYPE "$source")" == disk ]]; then parent=$(basename "$source"); fi
if [[ -n "$parent" ]]; then
  partitions=$(lsblk -lnpo NAME,TYPE "/dev/$parent") || exit 2
  while read -r device type; do
    [[ "$type" == part && "$device" != "$source" ]] || continue
    target=$(findmnt -n -o TARGET --source "$device" || true)
    if [[ -z "$target" ]]; then
      target="$runtime/$(basename "$device")"
      install -d -m 0700 "$target"
      if ! mount -o ro,nodev,nosuid,noexec "$device" "$target"; then continue; fi
      mounts+=("$target")
    fi
    collect "$target"
  done <<< "$partitions"
fi

# Disposable VMs attach an answers CD alongside the unchanged release ISO.
if [[ -e "$answers_device" ]]; then
  target="$runtime/media"
  install -d -m 0700 "$target"
  mount -o ro,nodev,nosuid,noexec "$answers_device" "$target" || exit 2
  mounts+=("$target")
  collect "$target"
fi

if [[ "${#files[@]}" == 0 ]]; then exit 1; fi
if [[ "${#files[@]}" != 1 ]]; then
  echo "Multiple floatlab-*.toml answers files found; keep exactly one before installing." >&2
  exit 2
fi
install -m 0600 "${files[0]}" "$runtime/answers.toml"
