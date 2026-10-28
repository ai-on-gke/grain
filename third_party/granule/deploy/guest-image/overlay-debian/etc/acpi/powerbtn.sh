#!/bin/sh
# acpid is the only thing in this guest listening for ACPI events (no
# logind/dbus), so this is what turns cloud-hypervisor's `vm.power-button`
# API call into an actual clean shutdown instead of granule's forced-
# shutdown fallback firing on every run.
exec systemctl poweroff
