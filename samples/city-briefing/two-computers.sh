#!/usr/bin/env bash
# City Briefing across two computers: the Researcher on a Linux machine of your network, kept up by
# systemd, and the Concierge on this Mac, which asks it over A2A. It does what TWO-COMPUTERS.md says,
# step by step, from the Mac: it reaches the Linux machine with ssh and runs its part there.
#
#   export LAB_IP=192.168.1.20 LAB_USER=me      # the Linux machine, and your user there
#   export ANTHROPIC_API_KEY=...                 # only for setup: it is written on the Linux machine
#   samples/city-briefing/two-computers.sh setup
#   samples/city-briefing/two-computers.sh ask Lisbon
#   samples/city-briefing/two-computers.sh status | logs | uninstall
#
# It needs, on the Mac: Metagente in ~/bin/metagente (or METAGENTE=path), gh logged in, ssh to the Linux
# machine. On the Linux machine: sudo, curl and openssl. Written for the bash of macOS (3.2) and Linux.

set -euo pipefail

VERSION="${METAGENTE_VERSION:-0.4.0}"
REPO="Ribeiro/metagente-go"
FETCH="uvx mcp-server-fetch==2026.8.18"                 # as in researcher.ag
SAMPLE_ADDRESS="http://127.0.0.1:8080/agents/Researcher" # as in concierge.ag
PORT=8443
TIMEOUT="${TIMEOUT_SECONDS:-180}"
CA_NAME="Metagente Homelab CA"
LAB_DIR="metagente/city-briefing"                       # in the home of LAB_USER
MAC_DIR="${MAC_DIR:-$HOME/metagente-lab/concierge}"
METAGENTE="${METAGENTE:-$HOME/bin/metagente}"

die() { printf 'two-computers: %s\n' "$*" >&2; exit 1; }
say() { printf '\n== %s\n' "$*"; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is needed: $2"; }

usage() {
	sed -n '2,13p' "$0" | sed 's/^# \{0,1\}//'
	exit 2
}

# ---------- on the Mac ----------

config() {
	[ -n "${LAB_IP:-}" ] || die "set LAB_IP to the address of the Linux machine, for example: export LAB_IP=192.168.1.20"
	[ -n "${LAB_USER:-}" ] || die "set LAB_USER to your user on the Linux machine, for example: export LAB_USER=me"
	printf '%s' "$LAB_IP" | grep -Eq '^([0-9]{1,3}\.){3}[0-9]{1,3}$' || die "LAB_IP is not an IPv4 address: $LAB_IP"
	printf '%s' "$LAB_USER" | grep -Eq '^[a-z_][a-z0-9_.-]*$' || die "LAB_USER is not a user name: $LAB_USER"
	LAB_NET="${LAB_NET:-${LAB_IP%.*}.0}"
	TARGET="$LAB_USER@$LAB_IP"
	URL="https://$LAB_IP:$PORT/agents/Researcher"
}

# The values put in a command for the Linux machine are checked first (config), so they are
# expanded here on purpose.
# shellcheck disable=SC2029
lab() { ssh "$TARGET" "$@"; }

github_file() { gh api -H "Accept: application/vnd.github.raw" "repos/$REPO/contents/samples/city-briefing/$1" > "$1"; }

setup() {
	config
	need gh "install it from https://cli.github.com and run: gh auth login"
	need ssh "it comes with macOS"
	need shasum "it comes with macOS"
	need security "run this part on macOS"
	[ -x "$METAGENTE" ] || die "Metagente is not at $METAGENTE (set METAGENTE=path)"
	key="${ANTHROPIC_API_KEY:-}"
	[ -n "$key" ] || die "set ANTHROPIC_API_KEY in this terminal: setup writes it on the Linux machine"
	case "$key" in *[[:space:]]*) die "ANTHROPIC_API_KEY holds a space or a line break: put only the key in it" ;; esac

	say "The Linux machine ($TARGET)"
	case "$(lab uname -m)" in
		x86_64) arch=linux-amd64 ;;
		aarch64 | arm64) arch=linux-arm64 ;;
		*) die "the Linux machine is not x86_64 or aarch64" ;;
	esac
	echo "$arch"

	say "Downloading Metagente $VERSION for it, and the Researcher"
	work="$(mktemp -d)"
	trap 'rm -rf "$work"' EXIT
	(
		cd "$work"
		gh release download "v$VERSION" -R "$REPO" -p "metagente-$VERSION-$arch.tar.gz" -p SHA256SUMS
		grep "metagente-$VERSION-$arch.tar.gz" SHA256SUMS | shasum -a 256 -c -
		github_file researcher.ag
		github_file metagente.toml
		grep -q "$FETCH" researcher.ag || die "researcher.ag no longer starts $FETCH: update this script"
	)

	say "Copying to the Linux machine"
	lab "mkdir -p ~/$LAB_DIR"
	scp -q "$work/metagente-$VERSION-$arch.tar.gz" "$work/researcher.ag" "$work/metagente.toml" "$0" "$TARGET:$LAB_DIR/"
	# The key goes through ssh on standard input: never in a command line, never on the screen.
	printf '%s\n' "$key" | lab "umask 077; cat > ~/$LAB_DIR/.anthropic-key"

	say "Setting up the Researcher there (sudo may ask for the password of $LAB_USER)"
	ssh -t "$TARGET" "bash ~/$LAB_DIR/$(basename "$0") remote-setup $LAB_IP $LAB_NET $arch $VERSION $TIMEOUT"

	say "Trusting its authority on this Mac (sudo asks for the password of the Mac)"
	mkdir -p "$MAC_DIR"
	cd "$MAC_DIR"
	scp -q "$TARGET:$LAB_DIR/ca.crt" ca.crt
	(umask 077 && scp -q "$TARGET:$LAB_DIR/.token" .researcher-token)
	while security find-certificate -c "$CA_NAME" /Library/Keychains/System.keychain >/dev/null 2>&1; do
		sudo security delete-certificate -c "$CA_NAME" /Library/Keychains/System.keychain
	done
	sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain ca.crt

	say "Setting up the Concierge in $MAC_DIR"
	github_file concierge.ag
	github_file metagente.toml
	grep -q "$SAMPLE_ADDRESS" concierge.ag || die "concierge.ag no longer reaches $SAMPLE_ADDRESS: update this script"
	sed -i '' "s|$SAMPLE_ADDRESS|$URL|" concierge.ag
	sed -i '' "s/^timeout_seconds = [0-9]*/timeout_seconds = $TIMEOUT/" metagente.toml
	"$METAGENTE" trust --yes concierge.ag

	status
	say "Ready. Ask with: $0 ask Lisbon"
}

ask() {
	config
	[ -n "${1:-}" ] || die "say which city: $0 ask Lisbon"
	[ -f "$MAC_DIR/.researcher-token" ] || die "run setup first"
	cd "$MAC_DIR"
	if [ -z "${ANTHROPIC_API_KEY:-}" ]; then
		# The key that setup wrote on the Linux machine, read without showing it.
		ANTHROPIC_API_KEY="$(lab "grep '^ANTHROPIC_API_KEY=' ~/$LAB_DIR/.env | cut -d= -f2-")"
		export ANTHROPIC_API_KEY
	fi
	RESEARCHER_TOKEN="$(cat .researcher-token)"
	export RESEARCHER_TOKEN
	time "$METAGENTE" run concierge.ag "city=$*"
}

status() {
	config
	say "The Researcher, from this Mac ($URL)"
	without="$(curl -s -o /dev/null -w '%{http_code}' "$URL/.well-known/agent-card.json" || true)"
	with="000"
	if [ -f "$MAC_DIR/.researcher-token" ]; then
		with="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $(cat "$MAC_DIR/.researcher-token")" "$URL/.well-known/agent-card.json" || true)"
	fi
	echo "without the token: $without (401 expected); with it: $with (200 expected)"
	if [ "$without" = "000" ]; then
		echo "000: the Mac does not reach it, or does not trust its certificate"
	fi
	say "The service on the Linux machine"
	lab "systemctl --user is-active researcher; journalctl --user -u researcher -n 3 --no-pager -o cat" || true
}

logs() {
	config
	ssh -t "$TARGET" "journalctl --user -u researcher -f -o cat"
}

uninstall() {
	config
	say "The Linux machine"
	ssh -t "$TARGET" "test -f ~/$LAB_DIR/$(basename "$0") && bash ~/$LAB_DIR/$(basename "$0") remote-uninstall || true"
	say "This Mac"
	while security find-certificate -c "$CA_NAME" /Library/Keychains/System.keychain >/dev/null 2>&1; do
		sudo security delete-certificate -c "$CA_NAME" /Library/Keychains/System.keychain
	done
	rm -rf "$MAC_DIR"
	echo "removed the authority from the keychain, and $MAC_DIR"
}

# ---------- on the Linux machine (called by setup and uninstall) ----------

remote_setup() {
	ip="$1" net="$2" arch="$3" version="$4" timeout="$5"
	export PATH="$HOME/.local/bin:/usr/local/bin:$PATH"
	cd "$HOME/$LAB_DIR"

	say "Metagente $version"
	tar -xzf "metagente-$version-$arch.tar.gz"
	sudo install "metagente-$version-$arch/metagente" /usr/local/bin/metagente
	metagente --version

	say "uv, and the fetch server once (the first time it is downloaded)"
	command -v uvx >/dev/null 2>&1 || curl -LsSf https://astral.sh/uv/install.sh | sh
	uvx --version
	$FETCH --help >/dev/null

	say "The certificate for $ip"
	if [ -f server.crt ] && openssl x509 -in server.crt -noout -text | grep -q "IP Address:$ip\$" &&
		openssl x509 -in server.crt -noout -checkend 2592000 >/dev/null; then
		echo "kept: it is for $ip and valid for more than 30 days"
	else
		openssl req -x509 -newkey rsa:2048 -nodes -days 3650 -subj "/CN=$CA_NAME" \
			-addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" \
			-addext "nameConstraints=critical,permitted;IP:$net/255.255.255.0" -keyout ca.key -out ca.crt 2>/dev/null
		openssl req -newkey rsa:2048 -nodes -subj "/CN=$ip" -keyout server.key -out server.csr 2>/dev/null
		printf 'subjectAltName=IP:%s\nextendedKeyUsage=serverAuth\n' "$ip" > ext.cnf
		openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 825 -extfile ext.cnf -out server.crt 2>/dev/null
		chmod 600 ca.key server.key
		echo "made, by an authority limited to $net/24"
	fi

	say "The token, the key and the approval"
	sed -i "s/^timeout_seconds = [0-9]*/timeout_seconds = $timeout/" metagente.toml
	metagente trust --yes researcher.ag
	umask 077
	[ -s .token ] || metagente token > .token
	[ -s .anthropic-key ] || die "the key did not arrive"
	printf 'METAGENTE_TOKEN=%s\nANTHROPIC_API_KEY=%s\n' "$(cat .token)" "$(cat .anthropic-key)" > .env
	rm -f .anthropic-key
	if command -v ufw >/dev/null 2>&1 && sudo ufw status | grep -q "Status: active"; then
		sudo ufw allow from "$net/24" to any port "$PORT" proto tcp
	fi

	say "The service"
	mkdir -p "$HOME/.config/systemd/user"
	cat > "$HOME/.config/systemd/user/researcher.service" <<EOF
[Unit]
Description=Metagente Researcher
After=network-online.target

[Service]
WorkingDirectory=%h/$LAB_DIR
EnvironmentFile=%h/$LAB_DIR/.env
Environment=PATH=%h/.local/bin:/usr/local/bin:/usr/bin:/bin
ExecStart=/usr/local/bin/metagente serve researcher.ag --public --tls-cert server.crt --tls-key server.key --host $ip:$PORT --port $PORT
Restart=on-failure

[Install]
WantedBy=default.target
EOF
	sudo loginctl enable-linger "$(id -un)"
	systemctl --user daemon-reload
	systemctl --user enable researcher >/dev/null 2>&1
	systemctl --user restart researcher
	for _ in 1 2 3 4 5 6 7 8 9 10; do
		if [ "$(curl -sk -o /dev/null -w '%{http_code}' "https://127.0.0.1:$PORT/" || true)" != "000" ]; then
			systemctl --user is-active researcher
			return 0
		fi
		sleep 1
	done
	journalctl --user -u researcher -n 20 --no-pager -o cat
	die "the Researcher did not start"
}

remote_uninstall() {
	systemctl --user disable --now researcher 2>/dev/null || true
	rm -f "$HOME/.config/systemd/user/researcher.service"
	systemctl --user daemon-reload 2>/dev/null || true
	rm -rf "${HOME:?}/$LAB_DIR"
	echo "removed the service and ~/$LAB_DIR; Metagente stays in /usr/local/bin and uv in ~/.local/bin"
}

case "${1:-}" in
	setup) setup ;;
	ask) shift; ask "$@" ;;
	status) status ;;
	logs) logs ;;
	uninstall) uninstall ;;
	remote-setup) shift; remote_setup "$@" ;;
	remote-uninstall) remote_uninstall ;;
	*) usage ;;
esac
