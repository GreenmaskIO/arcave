#!/bin/sh
# arcave's stdio transport: restic reaches its repository through arcave over a
# docker exec stream — no listener, no network, any daemon arcave's API reaches.
#
#   restic ─stdin──▶ relay ─▶ req  FIFO ─▶ bridge ─exec stdout─▶ arcave (REST, HTTP/2)
#   restic ◀stdout── relay ◀─ resp FIFO ◀─ bridge ◀─exec stdin── arcave
#
# One script, three roles, each its own process in the restic container:
#
#   entrypoint  the container's PID1. Execs restic with this script as its
#               rclone program, so restic speaks its REST protocol over HTTP/2
#               on the relay's stdin and stdout.
#   relay       what restic starts as that rclone program ("<script> relay
#               <remote>"; the remote is ignored).
#   bridge      what arcave starts with docker exec once the container runs; its
#               stdout and stdin are the stream arcave serves the repository on.
#
# arcave copies this file into the container after it is created and before it
# starts (cdc.StdioTransport), and runs it by that absolute path; the FIFOs live
# beside it. The image needs /bin/sh, mkfifo and cat — busybox's will do. The
# bridge and the relay end when their stdin does, or their peer goes; the
# container's exit ends whatever is left.
set -eu

case $0 in
/*) ;;
*)
	echo "arcave-stdio: run by absolute path, not '$0'" >&2
	exit 2
	;;
esac

dir=${0%/*}
req=$dir/req   # restic's requests:  relay  → bridge
resp=$dir/resp # arcave's responses: bridge → relay

# make_fifos makes both FIFOs, or finds them made. Every role calls it first, so
# no role depends on which the daemon runs first; opening a FIFO then blocks until
# its peer opens it, so no role waits for another either.
# Its own "File exists" is dropped; any other reason is printed on failure.
make_fifos() {
	reason=$(mkfifo "$req" "$resp" 2>&1) || true
	if [ ! -p "$req" ] || [ ! -p "$resp" ]; then
		echo "arcave-stdio: cannot make the FIFOs in $dir: $reason" >&2
		exit 1
	fi
}

# pump copies the FIFO $1 to stdout and stdin to the FIFO $2 until stdin ends or
# its peer goes, then stops the first copy and returns the second's status.
pump() {
	cat "$1" &
	status=0
	cat >"$2" || status=$?
	kill "$!" 2>/dev/null || true
	return "$status"
}

case "${1:-}" in
entrypoint)
	shift
	make_fifos
	exec restic -o rclone.program="$0" -o rclone.args=relay "$@"
	;;
relay)
	make_fifos
	pump "$resp" "$req"
	;;
bridge)
	make_fifos
	pump "$req" "$resp"
	;;
*)
	echo "arcave-stdio: unknown role '${1:-}'" >&2
	exit 2
	;;
esac
