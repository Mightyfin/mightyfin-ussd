#!/bin/sh
set -eu
# This directory is owned exclusively by this supervised instance.
# The supervisor lock prevents two managed instances and survives exec.
data_dir=${USSD_DATA_DIR:?required}
mkdir -p "$data_dir"
exec 9>"$data_dir/supervisor.lock"
flock -n 9 || { echo 'USSD instance already running' >&2; exit 1; }
# An unclean host shutdown may leave the application's exclusive-create marker.
# Only clear it while holding the independent supervisor lock.
rm -f "$data_dir/demo.lock"
exec /home/mightyfin/.local/lib/mightyfin-ussd/ussd
