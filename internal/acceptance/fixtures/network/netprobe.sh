#!/bin/sh
# netprobe.sh — in-guest network reachability battery.
#
# Runs a fixed set of reachability probes from inside a guest VM and prints one
# `PROBE <name> <ok|fail> <detail>` line per probe, bracketed by sentinels so a
# host-side parser can extract them from login banners / motd noise. It is
# POSIX sh and branches on `uname` so the same script runs on Linux and macOS
# guests.
#
# Arguments (positional; pass "-" to skip a probe):
#   $1 hostIP      ICMP ping the VM host           -> probe "host"
#                  ("auto" discovers and pings the default gateway, which is
#                   the macOS host's router in Apple shared/NAT and vmnet modes)
#   $2 peerIP      ICMP ping a peer VM             -> probe "peer"
#   $3 dnsName     resolve a hostname (DNS only)   -> probe "dns"
#   $4 internetURL fetch a URL by name             -> probe "internet"
#   $5 internetIP  fetch http://IP/ (no DNS)       -> probe "internet_ip"
#
# Exit status is always 0; the result is in the PROBE lines, not the exit code.

OS=$(uname -s 2>/dev/null || echo unknown)

emit() { printf 'PROBE %s %s %s\n' "$1" "$2" "$3"; }

# default_gateway: the guest's default-route gateway, or empty if none.
default_gateway() {
	case "$OS" in
	Darwin)
		route -n get default 2>/dev/null | awk '/gateway:/ {print $2; exit}'
		;;
	*)
		if command -v ip >/dev/null 2>&1; then
			ip route show default 2>/dev/null | awk '/default/ {print $3; exit}'
		else
			netstat -rn 2>/dev/null | awk '/^default|^0\.0\.0\.0/ {print $2; exit}'
		fi
		;;
	esac
}

# primary_ipv4: the guest's primary global (non-loopback) IPv4 address.
primary_ipv4() {
	case "$OS" in
	Darwin)
		ifconfig 2>/dev/null | awk '/inet / && $2 !~ /^127\./ {print $2; exit}'
		;;
	*)
		ip -4 -o addr show scope global 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | head -n1
		;;
	esac
}

# subnet_host: the .1 address of the guest's primary IPv4 subnet — the host's
# address in Apple vmnet host mode (which provides no default gateway, so the
# guest reaches the host directly on the segment rather than via a router).
# Empty if no global IPv4 is found.
subnet_host() {
	primary=$(primary_ipv4)
	[ -n "$primary" ] && echo "$primary" | sed 's/\.[0-9]*$/.1/'
}

# host_target: the address to ping for the "host" probe. Prefers the default
# gateway (nat/bridged); falls back to the subnet host (vmnet host mode).
host_target() {
	gw=$(default_gateway)
	if [ -n "$gw" ]; then
		echo "$gw"
	else
		subnet_host
	fi
}

# ping_one <ip>: one ICMP echo with a short deadline. macOS ping uses -t for
# the overall timeout in seconds; Linux ping uses -W for the per-reply wait.
ping_one() {
	case "$OS" in
	Darwin) ping -c 1 -t 3 "$1" >/dev/null 2>&1 ;;
	*) ping -c 1 -W 3 "$1" >/dev/null 2>&1 ;;
	esac
}

# resolve_one <name>: resolve a hostname using whatever resolver tool exists,
# without fetching anything (isolates DNS from egress).
resolve_one() {
	if command -v getent >/dev/null 2>&1; then
		getent ahosts "$1" >/dev/null 2>&1 && return 0
	fi
	if command -v dscacheutil >/dev/null 2>&1; then
		dscacheutil -q host -a name "$1" 2>/dev/null | grep -q 'ip_address' && return 0
	fi
	if command -v host >/dev/null 2>&1; then
		host -W 3 "$1" >/dev/null 2>&1 && return 0
	fi
	if command -v nslookup >/dev/null 2>&1; then
		nslookup "$1" >/dev/null 2>&1 && return 0
	fi
	return 1
}

# fetch <url>: HTTP(S) GET with a hard timeout; success on a 2xx/3xx response.
fetch() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsS --max-time 8 -o /dev/null "$1" >/dev/null 2>&1 && return 0
		return 1
	fi
	if command -v wget >/dev/null 2>&1; then
		wget -q -T 8 -O /dev/null "$1" >/dev/null 2>&1 && return 0
		return 1
	fi
	return 1
}

echo '===NETPROBE-BEGIN==='
echo "uname=$OS"

if [ "$1" != "-" ]; then
	host_ip="$1"
	if [ "$host_ip" = "auto" ]; then
		host_ip=$(host_target)
	fi
	if [ -z "$host_ip" ]; then
		emit host fail "no host address (no gateway, no global IPv4)"
	elif ping_one "$host_ip"; then
		emit host ok "ping $host_ip"
	else
		emit host fail "ping $host_ip"
	fi
fi
if [ "$2" != "-" ]; then
	if ping_one "$2"; then emit peer ok "ping $2"; else emit peer fail "ping $2"; fi
fi
if [ "$3" != "-" ]; then
	if resolve_one "$3"; then emit dns ok "resolve $3"; else emit dns fail "resolve $3"; fi
fi
if [ "$4" != "-" ]; then
	if fetch "$4"; then emit internet ok "fetch $4"; else emit internet fail "fetch $4"; fi
fi
if [ "$5" != "-" ]; then
	if fetch "$5"; then emit internet_ip ok "fetch $5"; else emit internet_ip fail "fetch $5"; fi
fi

echo '===NETPROBE-END==='
