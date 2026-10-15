set -eu
runtime=/run/floatlab-installer
root=$runtime/root
dataset=floatlab/os/installer

case "$1" in
prepare)
  origin=$(zfs get -H -o value origin floatlab/os/current)
  case "$origin" in
    floatlab/os/*@base) ;;
    *) echo "Current OS has no pristine version snapshot" >&2; exit 1 ;;
  esac
  version=${origin%@base}
  case "${version#floatlab/os/}" in
    current|live|installer|next|*/*) echo "Invalid OS snapshot origin" >&2; exit 1 ;;
  esac
  test "$(zfs get -H -o value readonly "$version")" = on
  # Refuse leftovers; cleanup must finish before another runtime is created.
  if zfs list -H -o name "$dataset" >/dev/null 2>&1; then
    echo "Installer runtime already exists; inspect it before retrying" >&2
    exit 1
  fi
  mkdir -p "$root"
  # Keep the temporary ZFS mount out of other services' mount namespaces.
  mount --bind "$runtime" "$runtime"
  mount --make-private "$runtime"
  zfs clone -o mountpoint=legacy -o canmount=noauto -o readonly=off \
    -o org.zfsbootmenu:active=off -o org.floatlab:installer-runtime="$INVOCATION_ID" "$origin" "$dataset"
  mount -t zfs "$dataset" "$root"
  # Populate /etc without activating the installed system against the live host.
  chroot "$root" /run/current-system/sw/bin/cp -rs /run/current-system/etc/. /etc/
  for path in passwd group resolv.conf machine-id; do
    rm -f "$root/etc/$path"
    cp -L "/etc/$path" "$root/etc/$path"
  done
  mkdir -p "$root/bin" "$root/usr/bin" "$root/var" "$root/run/floatlab-installer"
  ln -s /run/current-system/sw/bin/bash "$root/bin/sh"
  ln -s /run/current-system/sw/bin/env "$root/usr/bin/env"
  ln -s /run "$root/var/run"
  touch "$root/run/floatlab-installer/finalizing"
  for path in /dev /proc /sys /floatlab /run/systemd /run/dbus /run/udev /run/log /run/floatlab; do
    mkdir -p "$path" "$root$path"
    mount --rbind "$path" "$root$path"
    mount --make-rslave "$root$path"
  done
  ;;
cleanup)
  if zfs list -H -o name "$dataset" >/dev/null 2>&1; then
    test "$(zfs get -H -o value org.floatlab:installer-runtime "$dataset")" = "$INVOCATION_ID"
    if mountpoint -q "$root"; then
      umount --recursive "$root"
    fi
    test "$(zfs get -H -o value mounted "$dataset")" = no
    zfs destroy "$dataset"
  fi
  if mountpoint -q "$runtime"; then umount "$runtime"; fi
  ;;
*) exit 1 ;;
esac
