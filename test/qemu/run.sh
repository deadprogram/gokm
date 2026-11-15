#!/bin/sh
# Boot a kernel in QEMU with a minimal initramfs that loads and unloads the
# given module, printing the kernel log on the serial console. Prints
# "TEST: PASS" / "TEST: FAIL" at the end.
#
# Usage: test/qemu/run.sh path/to/module.ko [param=value ...]
#   KERNEL=/boot/vmlinuz-linux  kernel image to boot (must match the kernel
#                               the module was built for)
set -eu

KO=$(realpath "$1")
shift
HERE=$(cd "$(dirname "$0")" && pwd)
KERNEL=${KERNEL:-/boot/vmlinuz-linux}
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

mkdir -p "$WORK/root/proc"
(cd "$HERE" && CGO_ENABLED=0 go build -o "$WORK/root/init" ./init)
cp "$KO" "$WORK/root/module.ko"
echo "$*" >"$WORK/root/params"
(cd "$WORK/root" && find . | cpio -o -H newc --quiet | gzip -1) >"$WORK/initramfs.cpio.gz"

ACCEL="-machine accel=tcg"
if [ -w /dev/kvm ]; then
	ACCEL="-enable-kvm -cpu host"
fi

# panic=-1 + -no-reboot: a kernel panic exits QEMU instead of hanging.
timeout 120 qemu-system-x86_64 $ACCEL -m 512 -smp 2 \
	-kernel "$KERNEL" -initrd "$WORK/initramfs.cpio.gz" \
	-append "console=ttyS0 loglevel=7 panic=-1 oops=panic" \
	-nographic -no-reboot
