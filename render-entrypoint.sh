#!/bin/sh
set -eu
DATA_ROOT="${LINDBLAD_DATA_ROOT:-/data}"
mkdir -p "$DATA_ROOT"
# The Render persistent disk is mounted at runtime. Make its root writable by
# the unprivileged application account without recursively touching huge data.
chown lrp:lrp "$DATA_ROOT"
exec gosu lrp /app/lindblad-route-planner-cloud "$@"
