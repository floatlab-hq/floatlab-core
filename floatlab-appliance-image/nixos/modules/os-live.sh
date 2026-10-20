# Runs in stage 1 after NixOS imports the root pool, before mounting /.
set -eu
# ZFSBootMenu supplies the environment it actually selected, even for one-shot boots.
selected=""
hostid=""
initpath=""
for argument in $(cat "${FLOATLAB_CMDLINE_FILE:-/proc/cmdline}"); do
  case "$argument" in
    spl.spl_hostid=0x*) hostid=${argument#spl.spl_hostid=0x} ;;
    init=*) initpath=${argument#init=} ;;
    root=zfs:*) test -z "$selected"; selected=${argument#root=zfs:} ;;
    root=ZFS=*) test -z "$selected"; selected=${argument#root=ZFS=} ;;
  esac
done
case "$selected" in
  floatlab/os/current|floatlab/os/next|floatlab/os/previous|floatlab/os/recovery) ;;
  *) echo "Invalid or missing selected OS boot environment" >&2; exit 1 ;;
esac
origin=$(zfs get -H -o value origin "$selected")
case "$origin" in
  floatlab/os/*@base) ;;
  *) echo "Selected OS has no pristine version snapshot" >&2; exit 1 ;;
esac
version=${origin%@base}
case "${version#floatlab/os/}" in
  current|live|next|previous|recovery|*/*) echo "Invalid OS snapshot origin" >&2; exit 1 ;;
esac
test "$(zfs get -H -o value readonly "$selected")" = on
test "$(zfs get -H -o value readonly "$version")" = on
zfs list -H -t snapshot -o name "$origin" >/dev/null
case "$hostid" in ????????) ;; *) echo "Missing installed host identity" >&2; exit 1;; esac
case "$hostid" in *[!0-9a-f]*) exit 1;; esac
test "$(zfs get -H -o value org.floatlab:host-id "$selected")" = "$hostid"
test "$(zfs get -H -o value org.floatlab:host-id "$version")" = "$hostid"
test "$(zfs get -H -o value org.floatlab:version "$selected")" = "${version#floatlab/os/}"
test "$(zfs get -H -o value org.floatlab:init "$selected")" = "$initpath"
case "$initpath" in /nix/store/*/init) ;; *) exit 1;; esac
if zfs list -H -o name floatlab/os/live >/dev/null 2>&1; then
  test "$(zfs get -H -o value mounted floatlab/os/live)" = no
  # Never recursively destroy: unexpected snapshots or dependent clones require recovery.
  zfs destroy floatlab/os/live
fi
zfs clone -o mountpoint=legacy -o canmount=noauto -o readonly=off \
  -o org.zfsbootmenu:active=off "$origin" floatlab/os/live
