# Pathvector — Improvement TODO

Working list, grouped by theme. Big-ticket item first.

## 1. Management API (`pathvector serve`) — XodoPanel direct connectivity

**Done (v1):** `serve` command, unix-socket/TCP listener, bearer auth, health/
version/status/sessions endpoints, session PUT/PATCH/enable/disable/DELETE
persisted as `sessions.d/*.yml` fragments merged via `include:`, whole-config
`PUT /v1/config` push, `POST /v1/generate`, `show`-only `POST /v1/bird`,
`POST /v1/report`, and the periodic reporter + `pathvector report` CLI that
POSTs the exact payload XodoPanel's `/api/v1/bgp/report` accepts.

- [x] `POST /v1/reconcile` — one-call desired-state sync (diff + prune +
      optional rules) with a single apply.
- [x] `PUT/GET/DELETE /v1/rules` — API-managed `prefix-rules` set (per-prefix
      policy: reject/blackhole/prepend, panel actions no-transit/no-peers/
      no-export, template-targeted) stored as `_prefix-rules.yml` fragment.
- [x] `GET /v1/sessions/{name}/config` — desired-state read-back (redacted).
- [x] `bird-timeout` + dial/read deadlines on all BIRD socket commands.
- [x] `config_sha256` + `last_apply`/`last_apply_error` in `/v1/status`.
- [ ] TLS/mTLS for TCP listeners; separate read-only vs admin token scopes;
      rate limiting.
- [ ] `GET /v1/templates`, `PUT /v1/templates/{name}` — template CRUD (same
      fragment mechanism).
- [ ] `DELETE` / `disable` with graceful-shutdown drain first.
- [ ] Config push shouldn't drop `include:`/API keys silently — merge or warn.
- [ ] `GET/PUT /v1/prefixes` — manage global originated `prefixes:` without
      full config push.
- [ ] Flap/event push — POST immediately on session state transitions rather
      than waiting for the report interval.
- [ ] OpenAPI spec + docs page.

## 2. Config includes / composition

- [x] `include:` top-level directive (file or glob, e.g. `sessions.d/*.yml`) —
      merges `peers:`/`templates:`/`vrrp:`/`bfd:`/`mrt:` maps, errors on key
      collisions.
- [ ] `pathvector check` — parse + validate + dry-render without touching BIRD
      (usable by the panel as a pre-commit lint and in CI).
- [ ] `pathvector diff` — render new config, show unified diff vs live
      `bird-directory` before applying.
- [ ] Secrets: `password-file:` / `env:` indirection so MD5/keys never sit in YAML or
      API responses.

## 3. Apply safety

- [ ] Rollback on failed `configure` — keep last-good dir, auto-restore + `configure`
      back if BIRD rejects the new config (currently `bird -p` pre-validates but a
      failed reload leaves half-applied state).
- [ ] `configure soft` option for policy-only changes (avoid hard session resets).
- [ ] `--json` output mode on generate/status for machine consumption.
- [ ] Render provenance: stamp each generated file with config hash + pathvector
      version; `status` warns when running config ≠ generated output.
- [ ] Session drain helper: `pathvector drain <peer>` → tags graceful-shutdown /
      RFC8326 community, waits, then disables.

## 4. Correctness / hygiene debt

- [x] `process.Load`/`Run`/`peer`, `bird.Validate`/`MoveCacheAndReconfigure`,
      `templating.Write{,VRRP,UI}`, `util` globs, `irr.Update`, `peeringdb.Update`
      no longer `log.Fatal` — all return errors (required for API 422/500 paths).
- [x] `defer cancel()` inside the `for` loop in `irr.PrefixSet` — now per-iteration.
- [x] `bird.Read` panicked on socket EOF — the `recover()` wasn't deferred (dead
      code); now a real deferred recover → error.
- [x] `bird.ParseProtocol` index-out-of-range panic when a protocol has an empty
      Info column.
- [x] Bump `go` directive (1.18 → 1.23 for ServeMux method patterns). Deps still
      need a refresh pass (validator, cobra, golang.org/x/net, …).
- [ ] Consistent context.Context plumbing (PeeringDB/IRR queries, probe loop) so the
      daemon can shut down cleanly.
- [ ] Optimizer: `modifyPref` subtracts modifier on every run while alerts persist
      (ratchets local-pref down) — clamp to one depref per peer until it recovers;
      restore original pref on recovery; persist optimizer state across restarts.
- [ ] Optimizer: per-target results are merged per *peer*, not per probe-source —
      multi-source peers get averaged together; split cache by (peer, source, target).
- [ ] SortMap iteration in templates is nondeterministic in places — sort map keys in
      renders for stable diffs.

## 5. BGP/routing features worth adding

- [ ] Multiple RTR servers + `rpki` protocol redundancy; RPKI validity exposure in
      status/API.
- [ ] ASPA via RTR (BIRD 3) when available — keep `authorized-providers` static map
      as fallback.
- [ ] BMP monitoring instance option (route monitoring to an external collector).
- [ ] Flowspec/`blackhole` trigger helper — `pathvector blackhole <prefix>` pushing a
      tagged static (pairs with XodoPanel's `blackhole` per-prefix rule).
- [ ] `filtered-routes` introspection surfaced in API (works with keep-filtered).
- [ ] Per-neighbor options (today most knobs are per-peer across all neighbors):
      e.g. different password/local-pref per neighbor IP.
- [ ] Graceful BGP restart (`graceful restart` / LLGR knobs), `missing llgr`,
      stale-route handling.
- [ ] Auth: TCP-AO support alongside MD5 (kernel 5.x + BIRD support permitting).
- [ ] DNAME/`hostname` & `show route` export for a looking glass feed (pairs with the
      `looking-glass` repo).

## 6. Observability

- [ ] Prometheus exporter (`/metrics` on the API listener): session state, per-peer
      route counts in/out/filtered, optimizer probe latency/loss, last successful
      generate/apply timestamp, validation failures.
- [ ] Structured JSON logging option (`--log-format json`).
- [ ] Event/webhook emitter: on session state change, config apply, optimizer depref —
      POST to a configured URL (can be XodoPanel's webhook receiver directly).
- [ ] MRT → optional periodic upload of `mrt` dumps (already renderable).

## 7. Packaging / ops

- [ ] Ship systemd units (`pathvector.service`, `pathvector-optimizer.service`,
      `pathvector-api.service`) + tmpfiles for `/run/pathvector`.
- [ ] Debian package + repo publish (`distrib/repo-update.py` exists — wire to CI).
- [ ] Container image for lab/CI.
- [ ] SELinux/apparmor notes; run-as-non-root where possible (BIRD socket perms).

## 8. Tests / CI

- [ ] Revive `tests/bird-matrix` (docker BIRD versions) in GitHub Actions.
- [ ] Golden-file tests for generated BIRD output per feature flag.
- [ ] `golangci-lint` config + CI job; `go vet`; `staticcheck`.
- [ ] Integration test: API session CRUD → rendered config → BIRD validate in CI.
- [ ] PeeringDB test stub is present (`tests/peeringdb`) — extend coverage for
      auto-import-limits/auto-as-set/NVRS paths.

## 9. Docs

- [ ] Update docusaurus site for fork (module path, install source, Xodo additions).
- [ ] API reference page once `serve` lands (OpenAPI spec).
- [ ] Migration notes vs upstream pathvector (behavior deltas in this fork:
      import-order change — local-pref/communities first, then filters, blackhole,
      then RPKI; ASN-based local-pref; blackhole to /32 + /128; larger default
      limits; single-IP bounds).
