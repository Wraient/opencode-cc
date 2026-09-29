#!/usr/bin/env bash
# watch-stalls: proactive stall monitor for opencode-cc (muse-spark bridge).
# Checks (a) journal rename/recover/drop signatures and (b) the requests DB
# for the stall shape (tool_use chain -> tiny text-only end_turn) and errors.
# State in /tmp/stall-watch.state (max request id + last run ts) so each run
# reports only NEW activity. Exit 0 = quiet, 1 = needs attention.
set -u
DB="${STALL_DB:-/home/wraient/data/opencode-cc.db}"
STATE="${STALL_STATE:-/tmp/stall-watch.state}"
UNIT="${STALL_UNIT:-opencode-cc.service}"

last_id=0
if [ -f "$STATE" ]; then
  # shellcheck disable=SC1090
  . "$STATE" 2>/dev/null || true
fi

echo "--- journal (resolve/recover/drop/filter/resample) ---"
journalctl --user -u "$UNIT" --since "10 min ago" --no-pager 2>/dev/null \
  | grep -E 'resolved [0-9]+ upstream|recovered [0-9]+ nameless|dropped upstream|filtered [0-9]+ undeclared|short end_turn|toolless end_turn|resample' \
  | tail -n 15
echo "--- db: rows since id $last_id ---"
sqlite3 "$DB" "SELECT id, datetime(ts/1000,'unixepoch'), status, stop_reason, input_tokens, output_tokens, substr(error,1,70) FROM requests WHERE id > $last_id ORDER BY id DESC LIMIT 25"
echo "--- db: stall-shape candidates (end_turn, input>20000, output<150) ---"
sqlite3 "$DB" "SELECT id, datetime(ts/1000,'unixepoch'), input_tokens, output_tokens FROM requests WHERE id > $last_id AND status = 200 AND stop_reason = 'end_turn' AND input_tokens > 20000 AND output_tokens < 150 ORDER BY id DESC LIMIT 10"
echo "--- db: errors ---"
sqlite3 "$DB" "SELECT id, datetime(ts/1000,'unixepoch'), status, substr(error,1,80) FROM requests WHERE id > $last_id AND status >= 400 ORDER BY id DESC LIMIT 10"

new_max=$(sqlite3 "$DB" "SELECT COALESCE(max(id),0) FROM requests")
printf 'last_id=%s\n' "$new_max" > "$STATE"

# verdict for the caller: count new stall candidates + errors
stalls=$(sqlite3 "$DB" "SELECT count(*) FROM requests WHERE id > $last_id AND status = 200 AND stop_reason = 'end_turn' AND input_tokens > 20000 AND output_tokens < 150")
errs=$(sqlite3 "$DB" "SELECT count(*) FROM requests WHERE id > $last_id AND status >= 400")
echo "VERDICT new_stall_candidates=$stalls new_errors=$errs new_rows=$((new_max - last_id))"
[ "$stalls" -gt 0 ] || [ "$errs" -gt 0 ] && exit 1
exit 0
