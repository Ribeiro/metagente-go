# City Briefing across two computers

The [README](README.md) runs both agents on one computer. Here the **Researcher** runs on a Linux machine
on your network (a home server, say) and stays up, and the **Concierge** runs on your Mac and asks it over
A2A. This was done on 2026-10-06 with Metagente 0.3.2: the Researcher on Linux (x86_64), the Concierge on
macOS, on the same local network. The steps download 0.4.1; a token for each client (at the end) needs 0.4.0 or newer.

What changes from the one computer demo, and why:

- The Researcher has to listen **beyond its own computer**, and Metagente does that only with `--public`,
  which needs TLS: a certificate of its own.
- The Concierge never sends a token without `https://` to an address that is not its own computer.
- The Concierge checks that certificate against the authorities the Mac trusts, so the Mac is told to trust
  the small authority that signs it. That authority is limited to the addresses of your network (a name
  constraint): even trusted, it cannot vouch for any site on the internet.

In every block below, set the first lines to your values. `LAB_IP` is the address of the Linux machine
(`ip -4 addr` there); keep it fixed with a reservation in the DHCP of your router, since the certificate is
made for it. `LAB_NET` is your network with its last number at 0, and `LAB_USER` your user on the Linux
machine.

## With a script

[`two-computers.sh`](two-computers.sh) does the steps below from the Mac, reaching the Linux machine with
`ssh`. Running it again keeps what is already done (the certificate, the token, the approvals):

```bash
export LAB_IP=192.168.1.20 LAB_USER=me
export ANTHROPIC_API_KEY=...                       # only for setup; it is written on the Linux machine
samples/city-briefing/two-computers.sh setup      # sudo asks for a password there, and on the Mac
samples/city-briefing/two-computers.sh ask Lisbon # timed; the key is read from the Linux machine
samples/city-briefing/two-computers.sh status     # 401 without the token, 200 with it, the service
samples/city-briefing/two-computers.sh logs       # the log of the Researcher, as it comes
samples/city-briefing/two-computers.sh uninstall  # the service, the folders, the authority on the Mac
```

`setup` sets `timeout_seconds` to 180 on both sides (`TIMEOUT_SECONDS` changes it). The key goes to the
Linux machine through `ssh` on standard input, never in a command line. The steps, by hand:

## 1. On the Mac: download and copy

The release for Linux (`uname -m` on the Linux machine: `x86_64` is `linux-amd64`, `aarch64` is
`linux-arm64`) and the files of the Researcher:

```bash
LAB_IP=192.168.1.20; LAB_USER=me; ARCH=linux-amd64
ssh $LAB_USER@$LAB_IP 'mkdir -p ~/metagente/city-briefing'
mkdir -p ~/metagente-lab && cd ~/metagente-lab
gh release download v0.4.1 -R Ribeiro/metagente-go -p "metagente-*-$ARCH.tar.gz" -p SHA256SUMS --clobber
grep $ARCH SHA256SUMS | shasum -a 256 -c -
for f in researcher.ag metagente.toml; do
  gh api -H "Accept: application/vnd.github.raw" repos/Ribeiro/metagente-go/contents/samples/city-briefing/$f > $f
done
scp metagente-0.4.1-$ARCH.tar.gz researcher.ag metagente.toml $LAB_USER@$LAB_IP:metagente/city-briefing/
```

## 2. On the Linux machine: Metagente, uv, and the fetch server once

Everything from here to step 4 runs **on the Linux machine** (`ssh $LAB_USER@$LAB_IP`; `hostname` tells
where you are). Both computers may use `zsh`, so it is easy to run a block on the wrong one.

```bash
ARCH=linux-amd64
cd ~/metagente/city-briefing
tar -xzf metagente-0.4.1-$ARCH.tar.gz
sudo install metagente-0.4.1-$ARCH/metagente /usr/local/bin/metagente
metagente --version
curl -LsSf https://astral.sh/uv/install.sh | sh
uvx --version
uvx mcp-server-fetch==2026.8.18 --help
```

The last line matters: the first time, `uvx` downloads the fetch server (and maybe a Python), which can take
more than a minute. Done during the first question instead, it made the Concierge give up after its 90
seconds. Once it is in the cache, the Researcher answers in about 20 seconds.

## 3. On the Linux machine: the certificate

```bash
LAB_IP=192.168.1.20; LAB_NET=192.168.1.0
cd ~/metagente/city-briefing
openssl req -x509 -newkey rsa:2048 -nodes -days 3650 -subj "/CN=Homelab CA" \
  -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" \
  -addext "nameConstraints=critical,permitted;IP:$LAB_NET/255.255.255.0" -keyout ca.key -out ca.crt
openssl req -newkey rsa:2048 -nodes -subj "/CN=$LAB_IP" -keyout server.key -out server.csr
printf "subjectAltName=IP:$LAB_IP\nextendedKeyUsage=serverAuth\n" > ext.cnf
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 825 -extfile ext.cnf -out server.crt
chmod 600 ca.key server.key
```

`ca.key` never leaves this machine; only `ca.crt` goes to the Mac.

## 4. On the Linux machine: the token, the key, and a first run

```bash
LAB_IP=192.168.1.20; LAB_NET=192.168.1.0
cd ~/metagente/city-briefing
metagente trust researcher.ag
umask 077; metagente token > .token
printf 'METAGENTE_TOKEN=%s\nANTHROPIC_API_KEY=%s\n' "$(cat .token)" "PUT-THE-KEY-HERE" > .env
chmod 600 .env
nano .env
```

`trust` lists `starts the program: uvx mcp-server-fetch==2026.8.18`; answer `y`. In `nano`, put your
Anthropic key in place of `PUT-THE-KEY-HERE`, then save and leave (Ctrl-O, Enter, Ctrl-X). With `ufw`, open
the port to your network only: `sudo ufw allow from $LAB_NET/24 to any port 8443 proto tcp`.

The Researcher alone, then served by hand:

```bash
set -a; . ./.env; set +a
metagente run researcher.ag city=Lisbon
metagente serve researcher.ag --public --tls-cert server.crt --tls-key server.key --host $LAB_IP:8443 --port 8443
```

The banner says `Listening on [::]:8443, in HTTPS, for the Host: 192.168.1.20:8443` (`[::]` is every address,
IPv4 too). `--host` goes **with the port**: a client that connects to 8443 sends it in `Host`, and without it
every request gets `421` (the banner warns about it). Leave it running.

## 5. On the Mac: trust the authority, set up the Concierge

```bash
LAB_IP=192.168.1.20; LAB_USER=me
mkdir -p ~/metagente-lab/concierge && cd ~/metagente-lab/concierge
scp $LAB_USER@$LAB_IP:metagente/city-briefing/ca.crt .
scp $LAB_USER@$LAB_IP:metagente/city-briefing/.token .researcher-token
sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain ca.crt
for f in concierge.ag metagente.toml; do
  gh api -H "Accept: application/vnd.github.raw" repos/Ribeiro/metagente-go/contents/samples/city-briefing/$f > $f
done
sed -i '' "s|http://127.0.0.1:8080/agents/Researcher|https://$LAB_IP:8443/agents/Researcher|" concierge.ag
grep remote concierge.ag
~/bin/metagente trust concierge.ag
```

`trust` asks to approve the new address. Before asking the model anything, check the way between the two:

```bash
curl -s -o /dev/null -w '%{http_code}\n' https://$LAB_IP:8443/agents/Researcher/.well-known/agent-card.json
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $(cat .researcher-token)" https://$LAB_IP:8443/agents/Researcher/.well-known/agent-card.json
```

`401` without the token and `200` with it: the network works, and the Mac trusts the certificate (there is no
`--cacert`). `000` is the network or the certificate.

## 6. On the Mac: ask the Concierge

```bash
export ANTHROPIC_API_KEY=...                  # the key, as in "Running the demo"
export RESEARCHER_TOKEN=$(cat .researcher-token)
~/bin/metagente run concierge.ag city=Lisbon
```

Three lines about Lisbon, and on the Linux machine a line with `rpc=SendMessage message=research ...
result=ok`.

## 7. On the Linux machine: keep the Researcher up

Stop the one served by hand (Ctrl-C) and make it a service of your user:

```bash
LAB_IP=192.168.1.20
mkdir -p ~/.config/systemd/user
cat > ~/.config/systemd/user/researcher.service <<EOF
[Unit]
Description=Metagente Researcher
After=network-online.target

[Service]
WorkingDirectory=%h/metagente/city-briefing
EnvironmentFile=%h/metagente/city-briefing/.env
Environment=PATH=%h/.local/bin:/usr/local/bin:/usr/bin:/bin
ExecStart=/usr/local/bin/metagente serve researcher.ag --public --tls-cert server.crt --tls-key server.key --host $LAB_IP:8443 --port 8443
Restart=on-failure

[Install]
WantedBy=default.target
EOF
systemctl --user daemon-reload
systemctl --user enable --now researcher
sudo loginctl enable-linger $USER
systemctl --user status researcher --no-pager
```

`enable-linger` keeps it running when you are not logged in. Its log: `journalctl --user -u researcher -f`.

## More than one client

`METAGENTE_TOKEN` is one token for every client. To give the Mac and a notebook a token each, so that one is
taken away without the other and the log of the Researcher says who called (`client=mac`), use a token file
on the Linux machine instead:

```bash
cd ~/metagente/city-briefing
umask 077
printf 'mac %s\n' "$(cat .token)" > tokens        # the token the Mac already has
metagente token --name notebook >> tokens         # a new one, for the notebook
sed -i '/^METAGENTE_TOKEN=/d' .env                # only one of the two may say the token
sed -i 's|--port 8443$|--port 8443 --token-file tokens|' ~/.config/systemd/user/researcher.service
systemctl --user daemon-reload && systemctl --user restart researcher
```

From then on the file is read again when it changes, without a restart. To take the notebook away, remove
its line: write the file anew and rename it over the old one (`grep -v '^notebook ' tokens > tokens.new &&
mv tokens.new tokens`), and its next request gets `401`. These steps work the same after the script; its
`setup`, run again, writes back `METAGENTE_TOKEN` and the service without `--token-file`.

## Troubleshooting

| You see | What it means | What to do |
|---------|---------------|------------|
| `did not finish within 90 seconds` on the first question | The fetch server was being downloaded on the Linux machine | Run `uvx mcp-server-fetch==2026.8.18 --help` there once (step 2), then ask again |
| Every request gets `421`, or the banner has a `Note: ... has no port` | `--host` was given without `:8443` | Write `--host $LAB_IP:8443` |
| `curl` prints `000` | The Mac does not reach the machine, or does not trust the certificate | Check the firewall and that `ca.crt` was added to the keychain; on the Linux machine `ss -ltn` shows `:8443` |
| `zsh: command not found: metagente` on the Linux machine | The block ran on the Mac, or Metagente is not installed there | Check `hostname`; install as in step 2 |
| `the credential for Researcher would travel without encryption` | The address in `concierge.ag` is `http://` | It has to be `https://`, as the `sed` of step 5 writes it |

To undo it: on the Mac, `sudo security delete-certificate -c "Homelab CA" /Library/Keychains/System.keychain`;
on the Linux machine, `systemctl --user disable --now researcher` and remove `~/metagente/city-briefing`.
