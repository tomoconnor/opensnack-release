#!/usr/bin/env bash
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

set -euo pipefail

DURATION=$((8 * 60 * 60)) # 8 hours
END_TIME=$(( $(date +%s) + DURATION ))

BASE_DIR="$(pwd)/stress/multi-mode-stress-1"
LOG_DIR="$BASE_DIR/logs"
STATE_DIR="$BASE_DIR/state"

mkdir -p "$LOG_DIR" "$STATE_DIR"

i=0
while [ "$(date +%s)" -lt "$END_TIME" ]; do
  i=$((i+1))

  RUN_ID="$(uuid)"
  STATE_FILE="$STATE_DIR/state-$RUN_ID.tfstate"
  LOG_FILE="$LOG_DIR/run-$RUN_ID.log"

  echo "=== RUN $i ($RUN_ID) ==="

  TF_APPEND_USER_AGENT="custom-$RUN_ID" \
  OPENSNACK_NAMESPACE="ns-$RUN_ID" \
  tofu apply \
    -parallelism=1 \
    -auto-approve \
    -no-color \
    -state="$STATE_FILE" \
    >"$LOG_FILE" 2>&1
done
