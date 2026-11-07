#!/bin/sh
# capsrv.sh: the walk's droppable capability server -- grain's own
# config-map server under an id of its own -- which refuses to start while
# /home/debian/walk/capsrv.down exists, so a stopped server stays down.
[ -f /home/debian/walk/capsrv.down ] && { echo "capsrv: held down by the walk" >&2; exit 1; }
exec /home/debian/bin/grain capability config-map
