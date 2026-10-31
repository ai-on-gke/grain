#!/bin/sh
# cmp.sh <args...>: run `grain <args>` with the placed grain-sandbox and with
# a full cmd/grain copied in, print the first and report whether they match.
a=$(grain "$@" 2>&1); ea=$?
b=$(/home/debian/grain-full "$@" 2>&1); eb=$?
printf '%s\n' "$a" | head -"${CMP_LINES:-25}"
if [ "$a" = "$b" ] && [ $ea = $eb ]; then echo "[exit=$ea; full grain: identical]"; else echo "[exit=$ea; full grain exit=$eb, DIFFERS:]"; printf '%s\n' "$b" | head -"${CMP_LINES:-25}"; fi
