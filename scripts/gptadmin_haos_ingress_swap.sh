#!/bin/sh
# Swap one reviewed HAProxy file and gracefully reload its existing master. The caller owns the quorum and client preflights.
# Usage: sh -s -- [--server01-became] CANDIDATE_PATH EXPECTED_LIVE_SHA256 CANDIDATE_SHA256
set -eu
umask 077

fail() { echo "$1" >&2; exit 1; }
router=0
if [ "${1:-}" = --server01-became ]; then router=1; shift; fi
[ "$#" -eq 3 ] || fail "expected candidate path, live digest and candidate digest"
candidate=$1
before=$2
after=$3
for digest in "$before" "$after"; do
    [ "${#digest}" = 64 ] || fail "invalid digest"
    case "$digest" in *[!0-9a-f]*) fail "invalid digest";; esac
done
[ "$before" != "$after" ] || fail "expected distinct revisions"

binary=${GPTADMIN_EDGE_BIN:-/usr/sbin/haproxy}
if [ "$router" = 1 ]; then
    edge_dir=${GPTADMIN_EDGE_DIR:-/etc}
    live=$edge_dir/haproxy-became.cfg
    users=
    lock=$edge_dir/.gptadmin-wan-edge-lock
    backup=$edge_dir/gptadmin-wan-before.$before.cfg
    interval=1
else
    edge_dir=${GPTADMIN_EDGE_DIR:-/data}
    live=$edge_dir/haproxy.cfg
    users=$edge_dir/router-users.cfg
    # Same exclusion as the installed GPTAdmin public-route handoff.
    lock=$edge_dir/.gptadmin-writer44-edge-lock
    backup=$edge_dir/gptadmin-ingress-before.$before.cfg
    interval=.25
fi

hash_file() { sha256sum "$1" | cut -d' ' -f1; }
workers() {
    # OpenWrt omits /proc/PID/task/PID/children. Match the exact parent
    # and executable, including while the old worker drains connections.
    for child in $(pidof haproxy); do
        [ "$(awk '/^PPid:/{print $2}' "/proc/$child/status" 2>/dev/null || :)" = "$master" ] || continue
        if [ "$(readlink "/proc/$child/exe" || :)" = "$binary" ]; then
            awk '{print $1 "/" $22}' "/proc/$child/stat" 2>/dev/null || :
        fi
    done
}

[ -f "$live" ] && [ ! -L "$live" ] && [ -f "$candidate" ] && [ ! -L "$candidate" ] ||
    fail "missing or indirect edge configuration"
mkdir "$lock" || fail "GPTAdmin edge transaction already exists; inspect it"
uncertain=0
cleanup() {
    if [ "$uncertain" = 0 ]; then
        rm -f "$lock/next" "$lock/backup"
        rmdir "$lock"
    else
        echo "Inspect the installed digest, master and workers before releasing the transaction lock." >&2
    fi
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

[ "$(hash_file "$live")" = "$before" ] || fail "live configuration changed"
[ "$(hash_file "$candidate")" = "$after" ] || fail "candidate digest changed"
master=
for pid in $(pidof haproxy); do
    proc=/proc/$pid
    [ "$(readlink "$proc/exe" 2>/dev/null || :)" = "$binary" ] || continue
    parent=$(awk '/^PPid:/{print $2}' "$proc/status" 2>/dev/null) || continue
    [ "$(readlink "/proc/$parent/exe" 2>/dev/null || :)" != "$binary" ] || continue
    # Re-exec may append -sf/-x. Require the exact master mode and config set.
    tr '\000' '\n' < "$proc/cmdline" | awk -v live="$live" -v users="$users" '
        $0=="-W"{m=1} $0=="-f"{getline; files++; if($0==live)a++; if($0==users)b++}
        END{exit !(m && a==1 && files==(users=="" ? 1 : 2) && (users=="" || b==1))}' || continue
    [ -z "$master" ] || fail "ambiguous recovery master"
    master=${proc##*/}
done
[ -n "$master" ] || fail "configured recovery master unavailable"
if [ "$router" = 1 ] && [ "$edge_dir" = /etc ]; then
    managed=$(ubus call service list '{"name":"haproxy-became"}' | jsonfilter -e '@.*.instances.*.pid')
    [ "$managed" = "$master" ] || fail "procd service and configured master disagree"
fi
started=$(awk '{print $22}' "/proc/$master/stat")
old_workers=$(workers)
[ -n "$old_workers" ] || fail "no serving worker"

# Preserve owner/mode of the live file. Private backups are create-only.
cp -p "$live" "$lock/next"
cat "$candidate" > "$lock/next"
[ "$(hash_file "$lock/next")" = "$after" ] || fail "staged digest changed"
set -- -c -q -f "$lock/next"
if [ -n "$users" ]; then set -- "$@" -f "$users"; fi
"$binary" "$@" >/dev/null 2>&1 || fail "candidate failed HAProxy validation"
if [ ! -e "$backup" ]; then
    cat "$live" > "$lock/backup"
    ln "$lock/backup" "$backup"
fi
[ -f "$backup" ] && [ ! -L "$backup" ] && [ "$(hash_file "$backup")" = "$before" ] ||
    fail "before-image is unavailable"
[ "$(hash_file "$live")" = "$before" ] && [ "$(awk '{print $22}' "/proc/$master/stat")" = "$started" ] ||
    fail "master or live revision changed before swap"

uncertain=1
mv -f "$lock/next" "$live"
kill -USR2 "$master"
attempt=0
while [ "$attempt" -lt 40 ]; do
    sleep "$interval"
    [ "$(awk '{print $22}' "/proc/$master/stat")" = "$started" ] || fail "master changed during reload"
    for worker in $(workers); do
        case " $(echo "$old_workers" | tr '\n' ' ') " in *" $worker "*) continue;; esac
        [ "$(hash_file "$live")" = "$after" ] || fail "installed digest changed"
        uncertain=0
        echo "installed_sha256=$after master=$master worker=$worker backup=$backup"
        exit 0
    done
    attempt=$((attempt+1))
done
fail "new worker was not confirmed"
