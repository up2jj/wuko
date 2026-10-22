package ssh

// Helpers use positional arguments, never eval. They run with the SSH account's
// baseline environment; workflow environment overrides apply only to its command.
const initializeScript = `set -eu
[ "$(uname -s)" = Linux ] || { echo 'SSH executor requires Linux' >&2; exit 1; }
for tool in sh setsid stat head cat mktemp mkdir chmod mv ln rm sleep dirname sync; do
  command -v "$tool" >/dev/null || { echo "SSH executor requires $tool" >&2; exit 1; }
done
[ -x /bin/kill ] || { echo 'SSH executor requires /bin/kill' >&2; exit 1; }
case "$(stat --version)" in *GNU*) ;; *) echo 'SSH executor requires GNU coreutils' >&2; exit 1;; esac
cd -- "$1"
umask 077
mktemp -d /tmp/wuko-ssh.XXXXXXXXXX
`

// Keep the group leader alive after the command exits until the outer helper
// reaps it. Its PID stays reserved while descendants are signaled.
const groupScript = `run=$1
directory=$2
shift 2
read -r identity < /proc/$$/stat || exit 125
identity=${identity##*) }
set_identity() { shift 19; printf '%s %s\n' "$$" "$1"; }
set_identity $identity > "$run/owner.tmp"
mv -- "$run/owner.tmp" "$run/owner" || exit 125
[ ! -f "$run/cancel" ] || exit 125
interrupted=0
trap 'interrupted=1' HUP INT TERM
exec 3<&0
sh -c 'cd -- "$1" && . "$2" && shift 2 && exec "$@"' wuko "$directory" "$run/env" "$@" <&3 3<&- &
child=$!
# A trapped signal interrupts wait; the status must only be written once the command exits.
while :; do
  interrupted=0
  wait "$child"
  code=$?
  [ "$interrupted" = 1 ] && kill -0 "$child" 2>/dev/null || break
done
printf '%s\n' "$code" > "$run/status.tmp"
mv -- "$run/status.tmp" "$run/status" || exit 125
while :; do sleep 86400 & wait $!; done
`

// /proc start time, process group and session identify the still-owned leader.
// The cancel marker also covers cancellation before the command has launched.
// Grace counts 50ms steps. The leader deliberately outlives its command, so the
// command's completion status, not the leader's PID, ends the grace period early.
const stopScript = `set -eu
run=$1
grace=$2
[ -d "$run" ] || exit 0
: > "$run/cancel"
[ -f "$run/owner" ] || exit 0
read -r pid started < "$run/owner"
case "$pid:$started" in *[!0-9:]*|:*) exit 125;; esac
owned() {
  [ -r "/proc/$pid/stat" ] || return 1
  read -r identity < "/proc/$pid/stat" || return 1
  identity=${identity##*) }
  check_identity() {
    group=$3
    session=$4
    shift 19
    [ "$group" = "$pid" ] && [ "$session" = "$pid" ] && [ "$1" = "$started" ]
  }
  check_identity $identity
}
owned || exit 0
/bin/kill -TERM -- "-$pid" 2>/dev/null || true
while [ ! -f "$run/status" ] && [ "$grace" -gt 0 ]; do
  sleep 0.05
  grace=$((grace - 1))
done
if owned; then /bin/kill -KILL -- "-$pid" 2>/dev/null || true; fi
`

// Completion is out of band, leaving stdout/stderr exclusively for the command
// and distinguishing command exit 255 from SSH transport failure.
const runScript = `run=$1
directory=$2
group=$3
stop=$4
shift 4
[ ! -f "$run/cancel" ] || exit 0
exec 3<&0
setsid sh -c "$group" wuko "$run" "$directory" "$@" <&3 3<&- &
leader=$!
trap 'code=$?; trap - 0; grace=1; [ ! -f "$run/cancel" ] || grace=40; sh -c "$stop" wuko "$run" "$grace"; wait "$leader" 2>/dev/null; exit "$code"' 0
trap 'exit 143' HUP INT TERM
# Poll quickly for short commands, then back off so long commands do not fork 50 sleeps a second.
polls=0
while [ ! -f "$run/status" ]; do
  if ! kill -0 "$leader" 2>/dev/null; then exit 125; fi
  if [ "$polls" -lt 50 ]; then polls=$((polls + 1)); sleep 0.02; else sleep 0.2; fi
done
exit 0
`

const closeScript = `set -eu
root=$1
stop=$2
for run in "$root"/*; do
  [ -d "$run" ] || continue
  sh -c "$stop" wuko "$run" 1
done
rm -rf -- "$root"
`

// stopGrace allows a cancelled command two seconds, in stopScript's 50ms steps.
const stopGrace = "40"
