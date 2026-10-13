# Runs in stage 1 after NixOS imports the root pool, before mounting /.
set -eu
origin=$(zfs get -H -o value origin floatlab/os/current)
case "$origin" in
  floatlab/os/*@base) ;;
  *) echo "Current OS has no pristine version snapshot" >&2; exit 1 ;;
esac
version=${origin%@base}
case "${version#floatlab/os/}" in
  current|live|next|*/*) echo "Invalid OS snapshot origin" >&2; exit 1 ;;
esac
test "$(zfs get -H -o value readonly floatlab/os/current)" = on
test "$(zfs get -H -o value readonly "$version")" = on
zfs list -H -t snapshot -o name "$origin" >/dev/null
if zfs list -H -o name floatlab/os/live >/dev/null 2>&1; then
  test "$(zfs get -H -o value mounted floatlab/os/live)" = no
  # Never recursively destroy: unexpected snapshots or dependent clones require recovery.
  zfs destroy floatlab/os/live
fi
zfs clone -o mountpoint=legacy -o canmount=noauto -o readonly=off \
  -o org.zfsbootmenu:active=off "$origin" floatlab/os/live
