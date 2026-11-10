# Deploying medewid on NixOS

From a checkout to a running, monitored, backed-up `medewid` on the
brewhost, in order: flake input → files and certificates → module
configuration → switch → verify → metrics and backups. The module's
option reference is in [nix/module.nix](../nix/module.nix); the daemon's
flags and environment variables are in the [README](../README.md#daemon).

## Before you start

- **One client only.** Close or uninstall the vendor Status-Monitor apps
  on phones and desktops, and stop any `levacap` session. Starting or
  stopping the report stream is global machine state; a second client
  fights the daemon over it.
- **The machine's power switch cuts the controller too.** Switched off,
  leva! drops off the network, so `medewi_machine_connected` is 0 and
  medewid retries every 30 s. That is normal, not a fault (see
  [Metrics](#metrics)).
- **Never point anything at port 2323** (the bootloader). medewid
  refuses it; `MEDEWID_MACHINE_ADDR` is the text interface, normally
  port 23.

## 1. Flake input

```nix
# flake.nix of the host configuration
inputs.medewi = {
  url = "github:thomasbach-dev/medewi";
  inputs.nixpkgs.follows = "nixpkgs";
};

# in the host's modules list
inputs.medewi.nixosModules.medewid
```

The flake and CI track `nixos-unstable`. `follows` onto a release branch
works as long as its Go is at least the `go` version in `go.mod`
(currently 1.26; `nixos-26.05` builds it with the same `vendorHash`), but
only `nixos-unstable` is tested.

## 2. Files

All of these may be root-owned `0600` anywhere outside the Nix store
(`/etc/medewid/`, `/run/secrets/`, …): systemd reads them as root and
hands the daemon copies (`LoadCredential`). Never use a path literal
(`./medewid.key`): it would copy the file into the world-readable store,
and the module refuses it.

- **Status-Monitor XML** (`xmlFile`). It ships with the firmware release
  (`leva-<version>/…/xml file for status monitor/`). Its base name without
  `.xml` must equal the machine's `MC?` reply (`XML=leva!-65FCB201` →
  `leva!-65FCB201.xml`); otherwise medewid connects, logs `machine XML
  does not match the loaded spec`, sets `medewi_spec_mismatch` and starts
  no reports. After a firmware flash, swap the XML too. The capture
  runbook shows how to read `MC?` without the daemon:
  [docs/runbooks/capture-session.md](runbooks/capture-session.md).
- **Environment file** (`environmentFile`):

  ```sh
  MEDEWID_MACHINE_ADDR=machine.lan:23
  # optional, see "Event hook" below
  MEDEWID_EVENT_EXEC=/etc/medewid/hook.sh
  ```

- **Certificates** (`tlsCert`, `tlsKey`, `tlsClientCA`): next section.

## 3. Certificates

The API, the ops panel and `/metrics` all sit behind one mutual-TLS
listener. Rules the code imposes:

- **Every certificate the client CA signs is an admin.** Any certificate
  that chains to `tlsClientCA` may switch the machine (`POST /api/power`)
  and download the controller settings with the license key
  (`/api/backup/*`). Use a CA dedicated to medewid and sign only
  certificates for medewid's own clients. Never reuse a household CA whose
  certificates serve other services: the handshake is the barrier that
  keeps everyone else away from medewid's code.
- **The metrics CA only scrapes.** A certificate that chains to the
  optional `tlsMetricsCA` (and not to `tlsClientCA`) may only
  `GET /metrics`; everything else answers 403. Use it for Prometheus,
  above all when Prometheus sits outside the machine's Wifi: with an admin
  certificate, a compromised monitoring host could switch the machine and
  read the license key through medewid, which it otherwise cannot reach.
  The metrics CA must be its own root: neither signed by the client CA
  nor signing it, or a client could pick its role by the chain it sends.
  medewid refuses to start with nested CAs. These are the only two roles;
  there is no per-subject authorization.
- **TLS 1.3 only**, on both the daemon and `medewictl`.
- **The server certificate's SAN must match the name clients use** (the
  host in `MEDEWID_URL`, Prometheus' `server_name`, the panel's URL).
- **`medewictl` trusts only `MEDEWID_CTL_CA`**, no system roots: that file
  must hold the CA that signed the *server* certificate.
- **Certificates are read once, at start.** systemd copies the credential
  files when the unit starts, so a renewed certificate takes effect only
  after `systemctl restart medewid`. Reading the files per handshake would
  not help: the daemon only sees the copies.

### A private CA (recommended)

Run this on an admin machine. The CA's name constraints permit the
brewhost's name and exclude every IP address (a constraint only covers
the name types it lists), so a phone that trusts it cannot be fooled for
any other site, by name or by IP. If clients must reach the brewhost by
IP, put that IP in the server certificate's SAN and replace the two
exclusions with `permitted;IP:192.0.2.10/255.255.255.255`.

```sh
HOST=brewhost.lan
PKI=$HOME/medewid-pki

mkdir -p "$PKI" && cd "$PKI" && umask 077
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
  -keyout ca.key -out ca.crt -days 3650 -subj "/CN=medewid CA" \
  -addext "basicConstraints=critical,CA:TRUE,pathlen:0" \
  -addext "keyUsage=critical,keyCertSign,cRLSign" \
  -addext "nameConstraints=critical,permitted;DNS:$HOST,excluded;IP:0.0.0.0/0.0.0.0,excluded;IP:::/::"
# signs only Prometheus' certificate; nothing trusts it but medewid
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
  -keyout metrics-ca.key -out metrics-ca.crt -days 3650 -subj "/CN=medewid metrics CA" \
  -addext "basicConstraints=critical,CA:TRUE,pathlen:0" \
  -addext "keyUsage=critical,keyCertSign,cRLSign"

issue() { # name, extensions, CA (default ca)
  openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
    -keyout "$1.key" -out "$1.csr" -subj "/CN=$1"
  openssl x509 -req -in "$1.csr" -CA "${3:-ca}.crt" -CAkey "${3:-ca}.key" -CAcreateserial \
    -days 825 -out "$1.crt" -extfile <(printf '%s\n' "$2")
  rm "$1.csr"
}
issue server "subjectAltName=DNS:$HOST
extendedKeyUsage=serverAuth"
for c in medewictl phone; do
  issue "$c" "extendedKeyUsage=clientAuth"
done
issue prometheus "extendedKeyUsage=clientAuth" metrics-ca
# for the phone or browser: certificate, key and CA in one file
openssl pkcs12 -export -inkey phone.key -in phone.crt -certfile ca.crt \
  -name "medewid phone" -out phone.p12
```

The same `ca.crt` is the server's client CA (`tlsClientCA`), `medewictl`'s
`MEDEWID_CTL_CA` and Prometheus' `ca_file`; `metrics-ca.crt` is
`tlsMetricsCA`. Only `server.crt`, `server.key`, `ca.crt` and
`metrics-ca.crt` go to the brewhost. Renew the server certificate
before its 825 days run out, then restart the unit.

**Phones and browsers.** The ops panel needs a client certificate in the
device's own store: import `phone.p12`. medewid checks no revocation
list, so retiring a lost device's certificate means a new CA and
re-issuing every certificate; keep the device count small. To avoid a certificate warning, also
install `ca.crt` as a trusted CA; the name constraint keeps that harmless.
If an older phone refuses the `.p12`, re-export it with `-legacy`.

### An ACME (Let's Encrypt) server certificate

Possible, with two consequences: the SAN is the public name, and every
client's CA file must hold the ACME root (for Let's Encrypt, ISRG Root X1
and X2 in one PEM) instead of your private CA. Client certificates still
come from a private CA (`tlsClientCA`). Restart on renewal:

```nix
security.acme.certs."brewhost.example.org".reloadServices = [ "medewid" ];
```

The unit has no reload action, so this restarts it, which also re-reads
the credentials.

## 4. Module configuration

```nix
services.medewid = {
  enable = true;
  environmentFile = "/etc/medewid/medewid.env";
  xmlFile = "/etc/medewid/leva!-65FCB201.xml";
  tlsCert = "/etc/medewid/server.crt";
  tlsKey = "/etc/medewid/server.key";
  tlsClientCA = "/etc/medewid/ca.crt";
  tlsMetricsCA = "/etc/medewid/metrics-ca.crt"; # optional: scrape-only certificates
  # listen = ":8443";              # all interfaces
  # healthAddr = "127.0.0.1:9114"; # plaintext /healthz
  openFirewall = true;             # opens the listen port; off by default
  ipAddressAllow = [
    "192.168.178.0/24"             # machine, phones, DNS server
    "10.100.0.5/32"                # Prometheus
  ];
};
```

### The sandbox

The unit runs as a `DynamicUser` with no capabilities, a read-only
system, no home directories, no devices, a system-call filter and only
IPv4/IPv6/Unix sockets; the VM test keeps `systemd-analyze security
medewid` at or below 1.5. Consequences:

- The only writable path is `/var/lib/medewid` (a symlink to
  `/var/lib/private/medewid`), so `MEDEWID_DB` and `MEDEWID_RAW_DIR` must
  stay below it.
- `listen` must use a port above 1023.
- With `ipAddressAllow` set, the daemon can exchange packets only with
  loopback and the listed networks; everything else is dropped, so a
  compromised daemon cannot explore the rest of the network. The list
  must cover the machine, every API client, the DNS server if
  `MEDEWID_MACHINE_ADDR` is a name (unless the host resolves through
  `systemd-resolved` on loopback) and whatever the event hook talks to.
  Without the option no IP filter applies.

### Event hook

`MEDEWID_EVENT_EXEC` runs through `/bin/sh -c` per detector event, inside
the same sandbox, with:

| Variable | Value |
| --- | --- |
| `MEDEWI_EVENT` | `READY`, `STEAM_WINDOW`, `ECO` or `MACHINE_READY` |
| `MEDEWI_TEMP` | boiler temperature, e.g. `93.0` |
| `MEDEWI_SETPOINT` | setpoint, e.g. `93.0` |

The unit's `PATH` holds only coreutils, findutils, grep, sed and systemd;
add tools with `systemd.services.medewid.path = [ pkgs.curl ];`. The hook
cannot reach a desktop session's notification daemon (no home, no session
bus). Push notifications through a server the allowlist covers, e.g. an
ntfy instance on the brewhost:

```sh
#!/bin/sh
# /etc/medewid/hook.sh
curl -fsS -d "$MEDEWI_EVENT at $MEDEWI_TEMP °C" http://127.0.0.1:2586/espresso
```

The hook is killed when medewid stops; its output is logged only when
it fails.

## 5. Switch and verify

```sh
nixos-rebuild switch
journalctl -u medewid -f
```

Expected journal lines, in order:

1. `medewid serving addr=[::]:8443 … tls=true spec=leva!-65FCB201`
2. `connected addr=… firmware=3.2` (only while the machine is on)
3. `reports started`

Then:

```sh
MEDEWID_URL=https://brewhost.lan:8443
PKI=$HOME/medewid-pki

curl -fsS http://127.0.0.1:9114/healthz     # on the brewhost
MEDEWID_URL=$MEDEWID_URL MEDEWID_CTL_CA=$PKI/ca.crt \
  MEDEWID_CTL_CERT=$PKI/medewictl.crt MEDEWID_CTL_KEY=$PKI/medewictl.key \
  medewictl status
```

`connected: true` and `spec_ok: true` mean the daemon talks to the
machine with the right spec. `spec_ok: false` while connected means the
XML's name does not match the firmware (see [Files](#2-files)); the
daemon holds the connection without starting reports until you fix it.

The module also installs `levalog`, a wrapper that decodes the raw
traffic log with the module's `xmlFile` built in (root only: the
directory is `0700`):

```sh
sudo levalog decode --since=-5min   # the last five minutes on the wire
```

When the connection drops later, follow
[runbooks/troubleshoot-connection.md](runbooks/troubleshoot-connection.md).

## Metrics

`/metrics` is served on the mTLS listener only, so Prometheus needs a
client certificate. Issue it from the metrics CA (`tlsMetricsCA`) so it
can scrape but not switch the machine or read the backups:

```yaml
scrape_configs:
  - job_name: medewid
    scheme: https
    static_configs:
      - targets: ["brewhost.lan:8443"]
    tls_config:
      ca_file: /etc/prometheus/medewid/ca.crt
      cert_file: /etc/prometheus/medewid/prometheus.crt
      key_file: /etc/prometheus/medewid/prometheus.key
      server_name: brewhost.lan
```

On NixOS, keep the client key root-owned `0600` and hand it to
Prometheus the way the module hands medewid its files, via systemd
credentials:

```nix
systemd.services.prometheus.serviceConfig.LoadCredential = [
  "medewid.crt:/etc/prometheus/medewid/prometheus.crt"
  "medewid.key:/etc/prometheus/medewid/prometheus.key"
];
services.prometheus.scrapeConfigs = [{
  job_name = "medewid";
  scheme = "https";
  static_configs = [{ targets = [ "brewhost.lan:8443" ]; }];
  tls_config = {
    ca_file = "/etc/prometheus/medewid/ca.crt"; # public, any mode
    cert_file = "/run/credentials/prometheus.service/medewid.crt";
    key_file = "/run/credentials/prometheus.service/medewid.key";
    server_name = "brewhost.lan";
  };
}];
# The build-time `promtool check config` stats cert_file/key_file, which
# exist only inside the running unit.
services.prometheus.checkConfig = "syntax-only";
```

| Metric | Type | Meaning | Machine off / standby |
| --- | --- | --- | --- |
| `medewi_machine_connected` | gauge | 1 while a machine connection is up | 0 when off |
| `medewi_machine_released_until_seconds` | gauge | Unix time the release ends, 0 when not released | 0 |
| `medewi_spec_mismatch` | gauge | 1 while the machine's XML name differs from the loaded spec | 0 |
| `medewi_power_state{state}` | gauge | 1 for the current state: `active`, `eco`, `standby`, `unknown` | `unknown` when off, `standby` in standby |
| `medewi_ready` | gauge | boiler settled on the setpoint, machine active | 0 |
| `medewi_machine_ready` | gauge | the machine's own `[READY]`, until eco, standby or a disconnect | 0 |
| `medewi_boiler_celsius` | gauge | boiler temperature | **keeps its last value** |
| `medewi_setpoint_celsius` | gauge | boiler setpoint (drops in eco) | **keeps its last value** |
| `medewi_heater_percent` | gauge | heater output | **keeps its last value** |
| `medewi_shots_total` | counter | completed shots | — |
| `medewi_flushes_total` | counter | completed flushes | — |
| `medewi_pump_seconds_total` | counter | seconds of finished brews, truncated ones included | — |
| `medewi_volume_liters_total` | counter | liters of finished brews and wand draws | — |
| `medewi_water_draws_total` | counter | wand water draws | — |
| `medewi_water_liters_total` | counter | liters drawn at the wand | — |
| `medewi_report_lines_total` | counter | report lines received (~4/s while connected) | flat |
| `medewi_decode_errors_total` | counter | report lines skipped (checksum or decode error) | flat |
| `medewi_reconnects_total` | counter | connection attempts after the first, failed ones included | +2/min while off |
| `medewi_fragments_total` | counter | report/frame pieces cut off at a stream switch | — |
| `medewi_rawlog_dropped_total` | counter | raw-log events dropped, queue full | — |
| `medewi_rawlog_write_errors_total` | counter | raw-log write, flush or close errors | — |
| `medewi_rawlog_redacted_lines_total` | counter | received lines the raw log dropped by its allowlist | — |
| `medewi_rawlog_bytes` | gauge | raw-log directory size, measured at start and at each new file (hourly rotation, new session) | — |
| `process_start_time_seconds` | gauge | daemon start (Go process collector, with the other `process_*` metrics) | — |

Gate temperature panels on `medewi_machine_connected == 1` and
`medewi_power_state{state="standby"} == 0`; the stale values otherwise
draw flat lines. `medewi_fragments_total` grows with display sampling and
is not an error signal; it counts the live connection too, not only
ended ones. All counters restart at 0 with the daemon:
`changes(process_start_time_seconds[1d])` shows restarts, and `rate()`
and `increase()` already handle the resets. Because switching the machine off disconnects
it, a disconnect alone is not alertable.

A release closes the connection, so panels gated on
`medewi_machine_connected == 1` go blank while the machine is released;
`medewi_machine_released_until_seconds > 0` tells that apart from a
failure. The `MedewidConnectionFlapping` alert below counts a release as
two changes (down on release, up on reclaim), so a few releases within an
hour can trip it.

### Suggested alerts

```yaml
groups:
  - name: medewid
    rules:
      - alert: MedewidDown            # the daemon itself, not the machine
        expr: up{job="medewid"} == 0
        for: 5m
      - alert: MedewidSpecMismatch    # firmware flashed, XML stale
        expr: medewi_spec_mismatch == 1
        for: 5m
      - alert: MedewidDecodeErrors
        expr: increase(medewi_decode_errors_total[15m]) > 10
      - alert: MedewidConnectionFlapping  # Wi-Fi trouble while the machine is on
        expr: changes(medewi_machine_connected[1h]) > 6
      - alert: MedewidRawLogFailing
        expr: increase(medewi_rawlog_write_errors_total[1h]) > 0 or increase(medewi_rawlog_dropped_total[1h]) > 0
```

## Releasing the machine

To run something else against the machine (a conformance run, a
firmware flash, the vendor's Status Monitor), release it instead of
stopping the service:

```sh
medewictl release              # default +1h; units h, min, s: --until=+90min
medewictl reclaim              # early, when you are done
```

`medewictl release` returns only after medewid has closed its
connection, so `medewictl release && levacap …` is safe. While released,
medewid sends nothing to the machine, not even at startup or shutdown,
and refuses power, backup and mark requests with 409. The release is
stored in the database, so it survives a restart of medewid and ends at
the given time by itself. A brew open at the moment of the release is
kept as `truncated-released`. If medewid cannot read the stored release
at startup, it refuses to start rather than risk grabbing the machine
(fail closed); fix or restore the database first.

| Endpoint | Body | Reply |
| --- | --- | --- |
| `POST /api/release` | `{"until": "<RFC 3339>"}` | `{"ok", "until", "banner"}`; `banner` is whether the machine's banner returned once freed (`null` if unknown) |
| `POST /api/reclaim` | `{}` | `{"ok"}` |

`GET /api/status` carries `released_until`. Both endpoints use the same
mTLS client-CA protection as `POST /api/power`, and answer 500 when the
release state cannot be stored (the connection then stays as it was).

### Upgrading to schema v2

The first start of a release containing this feature migrates the
database to schema v2. It **deletes all brews of the removed kind
`interrupted` together with their curve points**; this cannot be undone
(back up first). Brew kinds `truncated-lost`, `truncated-released`,
`truncated-shutdown` and `truncated-crash` replace it. To inspect or
copy the rows beforehand:

```sh
DB=/var/lib/medewid/medewi.db
sudo nix shell nixpkgs#sqlite --command sqlite3 -readonly -header -column "$DB" "
  SELECT b.id, b.started_at, b.ended_at, b.duration_s, b.peak_bar, b.volume_ml,
         (SELECT COUNT(*) FROM brew_points p WHERE p.brew_id = b.id) AS points,
         b.ended_at = b.started_at AS crash
  FROM brews b WHERE b.kind = 'interrupted' ORDER BY b.id;"
```

After the migration an older medewid refuses to start ("unsupported
schema version 2"), so rolling back needs the database copy taken before
the upgrade.

## Backups

**Brew database.** `medewi.db` runs in WAL mode: committed data can sit in
`medewi.db-wal` for a while, so copying `medewi.db` alone can lose it.
Use SQLite's online backup instead, which writes one consistent file:

```nix
systemd.services.medewid-db-backup = {
  startAt = "daily";
  serviceConfig.Type = "oneshot";
  script = ''
    install -d -m 0700 /var/backup/medewid
    ${pkgs.sqlite}/bin/sqlite3 /var/lib/private/medewid/medewi.db \
      ".backup '/var/backup/medewid/medewi.db'"
  '';
};
```

Point the host's backup tool at `/var/backup/medewid`. If it backs up the
state directory directly instead, include `/var/lib/private/medewid` (the
real directory; `/var/lib/medewid` is only a symlink) and snapshot
`medewi.db` and `medewi.db-wal` together. The raw traffic log under
`raw/` is a seven-day troubleshooting buffer and needs no backup.

**Controller settings.** `medewictl backup --out DIR` dumps the machine's
settings (license key inside; files `0600`). They change only when
someone edits them on the machine, so run it after such changes and
before every firmware flash. A schedule is optional; the daemon refuses
the dump while the machine is off or a brew is running, so a scheduled
run fails harmlessly then.
