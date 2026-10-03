# XodoPanel ↔ Pathvector integration

Pathvector now runs a management API (`pathvector serve`) that accepts granular
JSON operations, so XodoPanel can drive BGP sessions and per-prefix routing
policy directly instead of rendering and scraping a whole `pathvector.yml`.

This document is the contract between the two sides. The bottom section lists
the XodoPanel-side work items — each one is a self-contained task you can hand
back to Devin later ("wire XodoPanel to …").

## Router-side setup

Add to `pathvector.yml`:

```yaml
api-listen: unix:///run/pathvector/api.sock   # or 0.0.0.0:8084 for TCP
api-key: <shared secret>                       # bearer auth; required for TCP
api-sessions-dir: sessions.d                   # relative to the config file's dir (NOT cwd)
report-url: https://panel.example.com/api/v1/bgp/report
report-key: <panel report key>
report-interval: 60
report-router: edge1.fra                       # defaults to hostname
bird-timeout: 60                               # seconds, BIRD socket deadline
# skip-peeringdb: true                         # skip PeeringDB lookups entirely (persisted)
# skip-irr: true                               # skip bgpq4/IRR lookups entirely (persisted)
# action-communities: true                     # built-in community library (see below)
# pop-id: 424                                  # site ID → `define pop_id`
include:
  - sessions.d/*.yml                           # required — API fragments land here
```

Then run `pathvector serve -c /etc/pathvector.yml` (systemd unit alongside
BIRD). Cron `pathvector generate` keeps working — API sessions live in
`sessions.d/*.yml` fragments merged by `include:`, so both paths see them.
`serve` refuses to start if `api-sessions-dir` is not covered by an `include:`
glob — without that, fragments would be written but never loaded.

## API surface (all JSON; `Authorization: Bearer <api-key>`)

Mutations are serialized, run the full render → `bird -p` → `configure`
pipeline, roll back on failure, and accept `?dry_run=1`. All mutating
endpoints also accept `?skip_pdb=1` and `?skip_irr=1` (see below).

| Method/Path | Purpose |
|---|---|
| `PUT /v1/sessions/{name}` | create/replace session. Body = peer fields (same keys as YAML): `asn`, `neighbors`, `template`, `description`, `password`, `local-pref`, `prepends`, `import-limit4/6`, `multihop`, `bfd`, `disabled`, `gateway`, `interface`, `bgp-med`, `comments`, … |
| `PATCH /v1/sessions/{name}` | merge fields; `null` deletes a key |
| `POST /v1/sessions/{name}/enable` `/disable` | toggle `disabled` |
| `DELETE /v1/sessions/{name}` | remove session + apply |
| `POST /v1/reconcile` | **desired-state sync** — see below |
| `PUT /v1/rules` | **replace the whole prefix-rules set** — see below |
| `GET /v1/rules` / `DELETE /v1/rules` | read/clear the rule set |
| `GET /v1/sessions` | all sessions: state, uptime, accepted/filtered/sent counts |
| `GET /v1/sessions/{name}` | + `received`/`filtered` prefix lists + `as_paths` (`?prefixes=N` caps enumeration; 0 = counts only) |
| `GET /v1/sessions/{name}/config` | stored fragment, `password` redacted — desired-state read-back |
| `GET /v1/status` | raw `show protocols all` + `config_sha256` + `last_apply`/`last_apply_error` (drift detection) |
| `GET /v1/config` / `PUT /v1/config` | full YAML pull/push — kept as the bootstrap/fallback path |
| `POST /v1/generate` | re-render + apply |
| `POST /v1/bird` | `{"command":"show …"}` — `show` only |
| `GET /v1/health` / `GET /v1/version` | liveness / versions |

### `POST /v1/reconcile`

The panel is source of truth. One call per sync — one render + one BIRD
reconfigure no matter how many sessions change:

```json
{
  "sessions": {
    "acme1": {"asn": 65002, "neighbors": ["192.0.2.2"], "template": "downstream", "local-pref": 150},
    "transit1": {"asn": 3356, "neighbors": ["192.0.2.20"], "template": "upstream"}
  },
  "prune": true,
  "rules": [{"session": "acme1", "prefix": "203.0.113.0/24", "action": "no-transit"}]
}
```

- Sessions missing from the set are deleted only when `prune: true`.
- `rules` is optional; absent = leave rule set alone, `[]` = clear it.
- Response: `{"ok":true,"created":[...],"updated":[...],"deleted":[...],"unchanged":[...]}`.
- Unchanged sessions are skipped (canonical YAML compare), so reconcile is
  cheap to run on a timer.

### Prefix rules (`PUT /v1/rules`)

Mirrors `BgpPolicy::catalog()` one-for-one. `session` = the *source* session
whose learned routes the rule matches (rendered as `proto ~ "NAME*"`).

| Panel rule | API `action` | Default targets | Renders (in matching peers' export filter) |
|---|---|---|---|
| `blackhole` | `blackhole` | all | `bgp_large_community.add((ASN, 0, 666));` |
| `no_transit` | `no-transit` | `upstream` | `if (proto ~ "SRC*" && net = PFX) reject;` |
| `no_peers` | `no-peers` | `peer`, `routeserver` | same |
| `no_export` | `no-export` | `upstream`, `peer`, `routeserver` | same |
| `prependN` | `prepend1`/`2`/`3` (or `prepend` + `count`) | all | `bgp_path.prepend(<source ASN>);` ×N |

Fields: `session`, `prefix` (CIDR), `action`, optional `targets` (template
names — override the defaults), optional `asn`/`count` for prepend overrides.
Generic `reject`/`blackhole`/`prepend` actions work too; `targets` selects by
template name, empty = all sessions.

### Skipping external lookups (`skip_pdb` / `skip_irr`)

Every apply re-queries PeeringDB (`auto-import-limits`, `auto-as-set`, NVRS)
and bgpq4 (`filter-irr`, `auto-as-set-members`) — that's the bulk of mutation
latency, and a dead pdb-cache/IRR box blocks *all* API writes. Two switches
degrade gracefully when they're down:

- **Per-request** (transient, e.g. an admin "apply anyway" button):
  `?skip_pdb=1` / `?skip_irr=1` on any mutating endpoint or `POST /v1/generate`.
- **Persistent**: `skip-peeringdb: true` / `skip-irr: true` in pathvector.yml
  (settable via `PUT /v1/config`), or `pathvector generate --skip-peeringdb --skip-irr`.

Semantics are fail-open: `auto-import-limits` peers get the default limits
(1500000/1000000), `auto-as-set` renders no as-set, `filter-irr` peers get no
IRR prefix-list check, NVRS/filter-as-set blocks are omitted. This is intended
as an *admin-side escape hatch* — XodoPanel should expose it only to admins
(a "skip external lookups" toggle on apply/reconcile), not to customers, since
it weakens prefix filtering on every affected session.

### Built-in community library (`action-communities`)

`action-communities: true` renders an ASN-parameterized community framework
into the global config — replaces hand-maintained `manual_global_filters.conf`.
Functions are callable from peer/template hooks (`pre-export`,
`post-import-filter`, `pre-export-final`) with `<pathvector.asn>` substitution:

- Action: `(ASN,911,asn)` no-announce-to-AS · `(ASN,739,ix)` no-announce-at-IX ·
  `(ASN,711-713,*)` prepend ×1-3 everywhere · `(ASN,72x,asn)` prepend-to-AS ·
  `(ASN,73x,ix)` prepend-at-IX
- Informational: `(ASN,411,x)` source/origin · `(ASN,412,ix)` learned-at-IX ·
  `(ASN,414,x)` learned-via-upstream · `(ASN,511,100-102)` RPKI valid/unknown/invalid
- All community numbers are defaults — override any role via `community-ids:`
  (`no-announce-as`, `no-announce-ix`, `prepend-general`, `prepend-as`,
  `prepend-ix`, `info-source`, `info-ix`, `info-upstream`, `info-rpki`,
  `downstream-tag`, `upstream-tag`). Prepend lists may be any length —
  `[a,b]` renders two prepend levels, `[a,b,c]` three.
- Pipelines: `import_communitys(peer_asn, pop_id, src_id)` (call with
  `pop_id`/`pop-id` and a per-template source id like 20/30/40/50),
  `downstream_communitys`, `export_communitys(peer_asn)`, `export_ix_communitys(ix_id)`
- `pop-id: N` renders `define pop_id = N;`. A hand-written `define pop_id` in
  `global-config` works instead — set one or the other (pathvector validates
  this and fails cleanly if both or neither are present)

### Reporting (router → panel)

- Auto-push: `report-url` + `report-key` + `report-interval` POSTs the exact
  payload `/api/v1/bgp/report` already accepts (`router`, `sessions[]` with
  `name`, `state`, `uptime`, `accepted`, `filtered_count`, `sent`,
  `received[]`, `filtered[]`, `as_paths{}`).
- Or one-shot `pathvector report --post -c /etc/pathvector.yml` (cron fallback).
- Or the panel can pull `GET /v1/sessions` itself.

## XodoPanel work items (ask Devin for these later)

1. **Pathvector client library** — `app/Libraries/PathvectorApi.php`: thin HTTP
   client over curl hitting the unix/TCP socket with bearer auth; methods
   `putSession`, `patchSession`, `deleteSession`, `setDisabled`, `reconcile`,
   `putRules`, `getSessions`, `getSessionConfig`, `getStatus`.
2. **Session CRUD hooks** — in `Admin/Bgp.php` + `Client/Bgp.php`, after the DB
   write succeeds, call the API: create/update → `reconcile` or `putSession`;
   status change → `enable`/`disable`; delete → `DELETE`. Map `bgp_sessions`
   columns → peer fields (name, peer_asn→`asn`, neighbors, template,
   description, password, local-pref, prepends, import limits, multihop, bfd).
   Decide error policy: DB-first + API-retry queue is safest.
3. **Prefix-rule sync** — wherever `bgp_prefix_rules` changes, push the whole
   set via `PUT /v1/rules` (translate `rule` column to `action` per the table
   above; join `bgp_sessions` for the `session` name). One call covers all
   sessions — replaces the `exportFilterLines()` render into templates.
4. **Report ingestion** — keep `POST /api/v1/bgp/report` as-is; set
   `report-url`/`report-key` per router. Alternatively poll `GET /v1/sessions`
   from the panel per router (needs TCP listen + firewalling).
5. **Drift/verification** — store `config_sha256` + `last_apply` from
   `GET /v1/status` per router; flag routers where the hash doesn't match the
   panel's last pushed state.
6. **Bootstrap path** — keep the existing `GET /api/pathvector.yml` renderer
   for initial provisioning; router can `PUT /v1/config` it once, then the
   panel switches to granular ops.
7. **Admin "apply anyway" toggle** — admin-only checkbox surfacing
   `?skip_pdb=1`/`?skip_irr=1` on reconcile/session writes for when PeeringDB
   or the IRR cache is unreachable; optionally set `skip-peeringdb`/`skip-irr`
   persistently via `PUT /v1/config` on chronically-isolated routers.
7. **Multi-router** — `bgp_sessions.router` already scopes sessions per router;
   the API calls must go to each router's own pathvector instance (per-router
   `api-listen`/`api-key`/`report-router` naming).
