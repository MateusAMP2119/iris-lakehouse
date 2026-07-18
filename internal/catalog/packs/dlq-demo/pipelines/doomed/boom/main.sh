#!/bin/sh
# boom - doomed-lane test pipeline: always dead-letters. Under the turn
# protocol a pipeline must answer its turn with a done frame on stdout; this
# script logs to stderr and exits 1 instead, so the engine records a mid-turn
# death and the run lands in the DLQ with reason failed (the stderr tail rides
# the dead letter's detail).
set -eu

echo "boom: going down on purpose" >&2
exit 1
