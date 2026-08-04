#!/usr/bin/env bash
set -euo pipefail

SERIAL="${ANDROID_ADB_SERIAL:-R5CR702SRFP}"
EXPECTED_SERIAL="R5CR702SRFP"
ADB_BIN="${ADB_BIN:-/usr/bin/adb}"
ADB_USER="${ADB_USER:-roomhacker}"
ADB_HOME="${ADB_HOME:-/home/roomhacker}"
ADB_RUN_AS_USER="${ADB_RUN_AS_USER:-1}"
BASE="${ANDROID_SHELLMCP_BASE:-/data/local/tmp/gptadmin}"
RUN="$BASE/run.sh"
RECEIPT_DIR="${AUTOSTART_RECEIPT_DIR:-/var/lib/gptadmin/android-s21-shellmcp-autostart}"

if [[ "$SERIAL" != "$EXPECTED_SERIAL" ]]; then
    printf 'status=error reason=unexpected_serial serial=%s\n' "$SERIAL"
    exit 2
fi

adb_base() {
    if [[ "$ADB_RUN_AS_USER" == "1" && "$(id -un)" != "$ADB_USER" ]]; then
        local uid gid
        uid="$(id -u "$ADB_USER")"
        gid="$(id -g "$ADB_USER")"
        setpriv --reuid="$uid" --regid="$gid" --init-groups env HOME="$ADB_HOME" "$ADB_BIN" "$@"
    else
        "$ADB_BIN" "$@"
    fi
}

adb_device() {
    adb_base -s "$SERIAL" "$@"
}

write_receipt() {
    local status="$1" parent_count="$2" child_count="$3" exact_executable_count="$4"
    mkdir -p "$RECEIPT_DIR"
    printf '{"schema":"gptadmin.android-shellmcp-autostart/v1","time":"%s","status":"%s","serial":"%s","parent_count":%s,"child_count":%s,"exact_executable_count":%s}\n' \
        "$(date -Is)" "$status" "$SERIAL" "$parent_count" "$child_count" "$exact_executable_count" >"$RECEIPT_DIR/latest.json"
}

device_state="$(adb_base devices | awk -v serial="$SERIAL" '$1==serial {print $2; exit}')"
if [[ "$device_state" != "device" ]]; then
    write_receipt waiting 0 0 0
    printf 'status=waiting serial=%s adb_state=%s\n' "$SERIAL" "${device_state:-missing}"
    exit 0
fi

read_counts() {
    adb_device shell "
        parent_pids=\$(ps -A -o PID,PPID,ARGS | awk -v run='$RUN' '(\$3==\"sh\" || \$3==\"/system/bin/sh\") && \$4==run {print \$1}');
        parent_count=\$(printf '%s\\n' \"\$parent_pids\" | awk 'NF {n++} END {print n+0}');
        canonical_child_count=0;
        for parent_pid in \$parent_pids; do
            for child_pid in \$(ps -A -o PID,PPID,NAME | awk -v parent=\"\$parent_pid\" '\$2==parent && \$3==\"shellmcp\" {print \$1}'); do
                exe=\$(readlink \"/proc/\$child_pid/exe\" 2>/dev/null || true);
                if [ \"\$exe\" = '$BASE/bin/shellmcp' ]; then
                    canonical_child_count=\$((canonical_child_count + 1));
                fi
            done
        done
        exact_executable_count=0;
        for child_pid in \$(pidof shellmcp 2>/dev/null || true); do
            exe=\$(readlink \"/proc/\$child_pid/exe\" 2>/dev/null || true);
            if [ \"\$exe\" = '$BASE/bin/shellmcp' ]; then
                exact_executable_count=\$((exact_executable_count + 1));
            fi
        done
        echo \"\$parent_count \$canonical_child_count \$exact_executable_count\"
    " | tr -d '\r'
}

read -r parent_count child_count exact_executable_count <<<"$(read_counts)"
if [[ "$parent_count" == "1" && "$child_count" == "1" && "$exact_executable_count" == "1" ]]; then
    write_receipt noop "$parent_count" "$child_count" "$exact_executable_count"
    printf 'status=noop serial=%s parent_count=1 child_count=1 exact_executable_count=1\n' "$SERIAL"
    exit 0
fi

printf 'status=reconcile reason=missing_or_duplicate parent_count=%s child_count=%s exact_executable_count=%s\n' "$parent_count" "$child_count" "$exact_executable_count"
adb_device shell "
    run='$RUN'; base='$BASE';
    for pid in \$(ps -A -o PID,PPID,ARGS | awk -v run=\"\$run\" '(\$3==\"sh\" || \$3==\"/system/bin/sh\") && \$4==run {print \$1}'); do
        for child in \$(ps -A -o PID,PPID,NAME | awk -v parent=\"\$pid\" '\$2==parent && \$3==\"shellmcp\" {print \$1}'); do
            kill \"\$child\" 2>/dev/null || true
        done
        kill \"\$pid\" 2>/dev/null || true
    done
    for pid in \$(pidof shellmcp 2>/dev/null || true); do
        exe=\$(readlink \"/proc/\$pid/exe\" 2>/dev/null || true)
        if [ \"\$exe\" = \"$BASE/bin/shellmcp\" ]; then kill \"\$pid\" 2>/dev/null || true; fi
    done
    sleep 1
    mkdir -p '$BASE/logs'
    nohup '$RUN' >'$BASE/logs/nohup.log' 2>&1 &
" >/dev/null

for _ in $(seq 1 20); do
    sleep 1
    read -r parent_count child_count exact_executable_count <<<"$(read_counts)"
    if [[ "$parent_count" == "1" && "$child_count" == "1" && "$exact_executable_count" == "1" ]]; then
        write_receipt started "$parent_count" "$child_count" "$exact_executable_count"
        printf 'status=started serial=%s parent_count=1 child_count=1 exact_executable_count=1\n' "$SERIAL"
        exit 0
    fi
done

write_receipt error "$parent_count" "$child_count" "$exact_executable_count"
printf 'status=error reason=duplicate_or_missing_after_start serial=%s parent_count=%s child_count=%s exact_executable_count=%s\n' "$SERIAL" "$parent_count" "$child_count" "$exact_executable_count"
exit 1
