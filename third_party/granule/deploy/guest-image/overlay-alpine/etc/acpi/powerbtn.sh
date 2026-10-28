#!/bin/sh
# acpid is the only thing in this guest listening for ACPI events (no
# logind/dbus), so this is what turns cloud-hypervisor's `vm.power-button`
# API call into an actual clean shutdown instead of granule's forced-
# shutdown fallback firing on every run.
#
# `poweroff` here is busybox's applet, not a raw reboot(2) syscall: with
# busybox as PID 1 (see /etc/inittab's "::shutdown:/sbin/openrc shutdown"
# line), it signals init rather than powering off directly, so this goes
# through the same "shutdown" runlevel -- stopping services, unmounting,
# syncing -- as an ordinary Alpine shutdown.
exec poweroff
