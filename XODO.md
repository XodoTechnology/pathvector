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
pipeline, roll back on failure, and accept `?dry_run=1`.

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
7. **Multi-router** — `bgp_sessions.router` already scopes sessions per router;
   the API calls must go to each router's own pathvector instance (per-router
   `api-listen`/`api-key`/`report-router` naming).
