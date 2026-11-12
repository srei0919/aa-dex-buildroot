#!/bin/sh
# Tests for etc/dexd/dexd-run. Builds a fake root in a temporary directory
# (DEXD_ROOT) with stub S93aa-proxy-rs, pidof and dexd. POSIX sh only.

HERE=$(cd "$(dirname "$0")" && pwd)
DEXD_RUN="$HERE/../../../board/rpi/rootfs_overlay/etc/dexd/dexd-run"

TMP=$(mktemp -d) || exit 1
trap 'rm -rf "$TMP"' EXIT
FAILED=0

# No real waiting in tests.
DEXD_POLL_SLEEP=0
DEXD_START_SLEEP=0
export DEXD_POLL_SLEEP DEXD_START_SLEEP

# Does this filesystem honour chmod 600?
touch "$TMP/modeprobe"
chmod 600 "$TMP/modeprobe"
if [ "$(ls -l "$TMP/modeprobe" | cut -c1-10)" = "-rw-------" ]; then
	MODES_OK=1
else
	MODES_OK=0
fi

newroot() {
	R=$(mktemp -d "$TMP/root.XXXXXX")
	mkdir -p "$R/uboot" "$R/etc/aa-proxy-rs" "$R/etc/init.d" "$R/bin" \
		"$R/usr/bin" "$R/data/dexd" "$R/tmp"
	: > "$R/tmp/s93.log"

	cat > "$R/etc/init.d/S93aa-proxy-rs" <<STUB
#!/bin/sh
echo "\$1" >> "$R/tmp/s93.log"
[ "\$1" = start ] && : > "$R/tmp/started"
exit 0
STUB
	cat > "$R/bin/pidof" <<STUB
#!/bin/sh
echo poll >> "$R/tmp/s93.log"
[ -e "$R/tmp/started" ] && exit 0
n=\$(cat "$R/tmp/polls" 2>/dev/null || echo 0)
if [ "\$n" -gt 0 ]; then
	echo \$((n - 1)) > "$R/tmp/polls"
	echo 1234
	exit 0
fi
exit 1
STUB
	cat > "$R/usr/bin/dexd" <<STUB
#!/bin/sh
echo usr >> "$R/tmp/ran"
exit 3
STUB
	chmod 755 "$R/etc/init.d/S93aa-proxy-rs" "$R/bin/pidof" "$R/usr/bin/dexd"

	printf '  aa_server_tcp_addr = ""\n  mitm = true\n  other = 1\n' \
		> "$R/etc/aa-proxy-rs/config.toml"
	DEXD_ROOT="$R"
	export DEXD_ROOT
}

run() { sh "$DEXD_RUN" "$@" >/dev/null 2>&1; }

check() { # check name function
	if ( "$2" ); then
		echo "ok $1"
	else
		echo "FAIL $1"
		FAILED=1
	fi
}

expect() { # expect what actual wanted
	if [ "$2" != "$3" ]; then
		echo "  $1: got [$2] wanted [$3]" >&2
		return 1
	fi
}

t_mode_aa() {
	newroot
	printf '\357\273\277AA\r\n' > "$R/uboot/dex-mode.txt"
	run prepare; expect "exit" $? 1 || return 1
	[ ! -e "$R/tmp/dexd/config.toml" ] || { echo "  config exists" >&2; return 1; }
}

t_mode_garbage() {
	newroot
	printf 'banana' > "$R/uboot/dex-mode.txt"
	run prepare; expect "exit" $? 0 || return 1
	[ -f "$R/tmp/dexd/config.toml" ] || return 1
}

t_no_mode_file() {
	newroot
	run prepare; expect "exit" $? 0 || return 1
}

t_config_rewrite() {
	newroot
	before=$(cksum < "$R/etc/aa-proxy-rs/config.toml")
	run prepare; expect "exit" $? 0 || return 1
	C="$R/tmp/dexd/config.toml"
	grep -qx '  aa_server_tcp_addr = "127.0.0.1:5288"' "$C" || { echo "  no addr line" >&2; return 1; }
	grep -qx '  mitm = false' "$C" || { echo "  no mitm line" >&2; return 1; }
	grep -qx '  other = 1' "$C" || { echo "  other line lost" >&2; return 1; }
	expect "lines" "$(wc -l < "$C" | tr -d ' ')" 3 || return 1
	expect "source" "$(cksum < "$R/etc/aa-proxy-rs/config.toml")" "$before" || return 1
}

t_config_missing_key() {
	newroot
	printf 'other = 1\nmitm = true' > "$R/etc/aa-proxy-rs/config.toml"
	run prepare; expect "exit" $? 0 || return 1
	C="$R/tmp/dexd/config.toml"
	expect "last line" "$(tail -n 1 "$C")" 'aa_server_tcp_addr = "127.0.0.1:5288"' || return 1
	grep -qx 'mitm = false' "$C" || { echo "  no mitm line" >&2; return 1; }
	grep -qx 'other = 1' "$C" || return 1
}

t_certs_copied() {
	newroot
	printf 'CERT\n' > "$R/uboot/md_cert.pem"
	printf 'KEY\n' > "$R/uboot/md_key.pem"
	run prepare; expect "exit" $? 0 || return 1
	expect "cert" "$(cat "$R/etc/aa-proxy-rs/md_cert.pem")" CERT || return 1
	expect "key" "$(cat "$R/etc/aa-proxy-rs/md_key.pem")" KEY || return 1
	if [ "$MODES_OK" = 1 ]; then
		for f in md_cert.pem md_key.pem; do
			expect "mode of $f" "$(ls -l "$R/etc/aa-proxy-rs/$f" | cut -c1-10)" "-rw-------" || return 1
		done
	else
		echo "skip certs-copied-mode (filesystem ignores modes)"
	fi
}

t_rejected_marker() {
	newroot
	: > "$R/data/dexd/selfsigned-rejected"
	run prepare; expect "exit without cert" $? 1 || return 1
	[ ! -e "$R/tmp/dexd/config.toml" ] || return 1
	printf 'CERT\n' > "$R/uboot/md_cert.pem"
	printf 'KEY\n' > "$R/uboot/md_key.pem"
	run prepare; expect "exit with cert" $? 0 || return 1
}

t_supervise_restores() {
	newroot
	run prepare; expect "prepare" $? 0 || return 1
	[ -f "$R/tmp/dexd/config.toml" ] || return 1
	run supervise
	[ ! -e "$R/tmp/dexd/config.toml" ] || { echo "  config remains" >&2; return 1; }
	expect "ran" "$(cat "$R/tmp/ran")" usr || return 1
	expect "calls" "$(tr '\n' ' ' < "$R/tmp/s93.log")" "stop poll start poll " || return 1
}

t_supervise_prefers_data() {
	newroot
	cat > "$R/data/dexd/dexd" <<STUB
#!/bin/sh
echo data >> "$R/tmp/ran"
exit 3
STUB
	chmod 755 "$R/data/dexd/dexd"
	run prepare
	run supervise
	expect "ran" "$(cat "$R/tmp/ran")" data || return 1
}

t_supervise_waits() {
	newroot
	echo 1 > "$R/tmp/polls"
	run prepare
	run supervise
	expect "order" "$(tr '\n' ' ' < "$R/tmp/s93.log")" "stop poll poll start poll " || return 1
}

check mode-aa t_mode_aa
check mode-garbage t_mode_garbage
check no-mode-file t_no_mode_file
check config-rewrite t_config_rewrite
check config-missing-key t_config_missing_key
check certs-copied t_certs_copied
check rejected-marker t_rejected_marker
check supervise-restores t_supervise_restores
check supervise-prefers-data t_supervise_prefers_data
check supervise-waits t_supervise_waits

exit $FAILED
