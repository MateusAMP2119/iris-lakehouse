#!/bin/sh
# aftershock - doomed-lane test pipeline: never runs. It gates on boom through
# depends_on, and boom always dead-letters, so aftershock reaches the DLQ with
# reason upstream_dead_lettered without its process ever starting. The script
# body only matters if boom is somehow fixed.
set -eu

echo "aftershock: upstream boom must have succeeded?!" >&2
exit 1
