<img alt="Pathvector Logo" src="https://pathvector.io/img/black-border.svg" height="200" />

Pathvector is a declarative edge routing platform that automates route optimization and control plane configuration with
secure and repeatable routing policy.

This is the **XodoTechnology fork**, extended with a management API (`pathvector serve`), config includes
(`include:` fragments), per-prefix routing policies (`prefix-rules`), a XodoPanel state reporter, and additional
peer options (`gateway`, `interface`, `bgp-med`, `comments`). See [XODO.md](XODO.md) for the XodoPanel
integration contract, [FEATURES.md](FEATURES.md) for the full feature list, and [TODO.md](TODO.md) for the roadmap.

## Features

* **Management API** — `pathvector serve` exposes JSON session CRUD, desired-state reconcile, per-prefix rules,
  sanitized config push/pull, read-only BIRD commands, and live status over a unix socket or authenticated TCP
* **Config fragments** — `include:` globs merge `sessions.d/*.yml`-style fragments, so API-managed sessions and
  hand-written config coexist; `pathvector generate` sees them too
* **Per-prefix routing policy** — `prefix-rules` (reject / blackhole / prepend / no-transit / no-peers / no-export)
  rendered into export filters, targetable by template
* **State reporting** — pushes session state + received/filtered prefixes + AS paths to a URL
  (e.g. XodoPanel `/api/v1/bgp/report`) on an interval, or pull via `GET /v1/sessions`
* Robust BGP route filtering with RPKI, IRR, and downstream AS cone, ASPA, never-via-RS and more
* Automatic configuration from PeeringDB
* Automatic route optimization by enriching the standard set of BGP attributes with latency and packet loss metrics
* Declarative configuration model: Want to track your changes? Just commit your file to version control.
* Data-plane agnostic: Pathvector works on servers, network switches, embedded devices, etc
* BFD and VRRP support
* Extensible Go plugin API

## Build from source

This fork is distributed as source; there are no prebuilt release packages.

Requires **Go ≥ 1.23** (the `go` directive in `go.mod`; any newer toolchain works):

```shell
git clone https://github.com/XodoTechnology/pathvector.git
cd pathvector
go build -o pathvector .
```

To stamp version info into the binary (recommended for production so `pathvector version` reports something useful):

```shell
go build -o pathvector \
  -ldflags "-X main.version=$(git describe --tags --always --dirty) \
            -X main.commit=$(git rev-parse --short HEAD) \
            -X main.date=$(date -u +%Y-%m-%d)" .
```

Builds without ldflags print a "development build" warning banner — cosmetic only.

## Runtime dependencies

Required:

* **BIRD ≥ 2.0.7** running with a control socket (default `/run/bird/bird.ctl`)

Optional, per feature:

* [bgpq4](https://github.com/bgp/bgpq4) — IRR prefix-list generation (`filter-irr`, `auto-as-set`)
* RTR server ([gortr](https://github.com/cloudflare/gortr) or Cloudflare's `rtr.rpki.cloudflare.com:8282`) — RPKI filtering
* [keepalived](https://github.com/acassen/keepalived) — VRRP output

The API has no extra dependencies (Go stdlib only).

## Install

```shell
install -m0755 pathvector /usr/local/sbin/pathvector
install -m0640 /dev/null /etc/pathvector.yml       # then edit it
mkdir -p /etc/bird /var/run/pathvector/cache
```

A minimal `pathvector.yml` for the API + reporter:

```yaml
asn: 64500
router-id: 192.0.2.1
prefixes: ["203.0.113.0/24"]

bird-directory: /etc/bird/
cache-directory: /var/run/pathvector/cache/
bird-socket: /run/bird/bird.ctl
bird-timeout: 60            # socket deadline for BIRD commands

include:                    # required for API-managed sessions
  - sessions.d/*.yml

api-listen: unix:///run/pathvector/api.sock   # or 0.0.0.0:8084 (api-key required on TCP)
api-key: CHANGE_ME
api-sessions-dir: sessions.d                  # relative to the config file

report-url: https://panel.example.com/api/v1/bgp/report
report-key: PANEL_KEY
report-interval: 60
report-router: edge1.example.com
```

Notes:

* `api-listen unix://…` is the recommended mode — the socket is created `0660` and needs no key (still honored if
  set). TCP listeners **require** `api-key`.
* Keep `api-key`, `report-key`, and peer `password` values out of world-readable config; `chmod 0640` the YAML.
  Session fragments in `sessions.d/` can contain passwords — same treatment.
* Generated files go to `bird-directory` (`bird.conf` + `AS*.conf`). Point BIRD's main config at the generated
  `bird.conf` (typically `include "/etc/bird/bird.conf";` is unnecessary — it *is* the main file).
* On Debian/Ubuntu install BIRD with `apt install bird2` (`bird -v` must report ≥ 2.0.7).

## Running

| Task | Command |
|---|---|
| One-shot generate + apply | `pathvector generate -c /etc/pathvector.yml` |
| Management API daemon | `pathvector serve -c /etc/pathvector.yml` (auto-starts the reporter when `report-url` is set) |
| One-shot status report to panel | `pathvector report --post -c /etc/pathvector.yml` |
| Protocol ops | `pathvector protocol restart|reload|enable|disable <name|all>` |

### systemd units

`/etc/systemd/system/pathvector.service` — the API daemon (also handles periodic reporting):

```ini
[Unit]
Description=Pathvector management API
After=network.target bird.service
Requires=bird.service

[Service]
ExecStart=/usr/local/sbin/pathvector serve -c /etc/pathvector.yml
Restart=always
RestartSec=5
ProtectSystem=strict
ReadWritePaths=/etc/bird /var/run/pathvector /etc/pathvector.yml /etc/sessions.d /run/pathvector

[Install]
WantedBy=multi-user.target
```

Optional timer to refresh IRR prefix lists / PeeringDB data (equivalent to the old `*/12 * * * *` cron):

`/etc/systemd/system/pathvector-generate.service`:

```ini
[Unit]
Description=Pathvector config regeneration

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/pathvector generate -c /etc/pathvector.yml
```

`/etc/systemd/system/pathvector-generate.timer`:

```ini
[Unit]
Description=Periodic Pathvector regeneration

[Timer]
OnCalendar=*-*-* 00,12:00:00
Persistent=true

[Install]
WantedBy=timers.target
```

```shell
systemctl daemon-reload
systemctl enable --now pathvector.service pathvector-generate.timer
```

## Differences from upstream natesales/pathvector

* `pathvector serve` + `pathvector report` commands and the whole `api-*`/`report-*`/`include:`/`prefix-rules`/
  `bird-timeout` config surface — upstream has none of these
* `pathvector protocol` command (BIRD `restart|reload|enable|disable`)
* New peer fields: `gateway`, `interface`, `bgp-med`, `comments`; new global `show-warning-message`
* Default import limits bumped to `1500000` (v4) / `1000000` (v6)
* `go.mod` requires Go 1.23 (ServeMux patterns; upstream was 1.18)
* Library code no longer calls `log.Fatal` mid-pipeline — `process.Run`, `bird.Validate`,
  `MoveCacheAndReconfigure`, `irr.Update`, `peeringdb.Update` etc. all return errors

Everything else — peer options, filtering families, optimizer, plugins, VRRP/BFD/MRT — matches upstream, so the
upstream [configuration manual](https://pathvector.io/docs/configuration) still applies (the regenerated field
reference is also in `docs/docs/configuration.md`, or run `pathvector docs`).

## Development

```shell
go build ./...
go vet ./...
go test ./pkg/... ./cmd/...
```

Some tests need external fixtures — `make test-sequence` sets them up: a Python PeeringDB stub on `localhost:5000`,
a `dummy0` interface, `PATHVECTOR_TEST=1`, and `bgpq4` on `PATH`. Without them, `pkg/irr`, `pkg/match`,
`pkg/peeringdb`, and `cmd` tests fail on connection errors (expected, environmental).
