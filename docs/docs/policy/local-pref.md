# Local Preference

## Setting local preference on import

By default, Pathvector sets the BGP local preference of every route imported from a peer to the peer's `local-pref`
(default `100`). Related options:

- `local-pref4` / `local-pref6` override `local-pref` for one address family.
- `set-local-pref: false` disables setting the local preference on import.
- `default-local-pref` sets BIRD's `default bgp_local_pref`, which only applies to routes received without a local
  preference (eBGP), instead of overwriting it.

## iBGP sessions

On iBGP, the local preference received from the neighbor usually carries policy decided at the network edge, so
Pathvector doesn't overwrite it by default. A session is treated as iBGP when the peer's `asn` equals the global `asn`
(or the peer's `local-asn`).

On an iBGP session, Pathvector only sets the local preference when one of `local-pref`, `local-pref4`, `local-pref6` or
`set-local-pref` is configured on the peer (or its template), or when `optimize-inbound` is enabled for the peer.

```yaml
asn: 65530
peers:
  Core:            # iBGP, local preference received from the neighbor is kept
    asn: 65530
    neighbors:
      - 192.0.2.2
  Core override:   # iBGP, local preference explicitly set to 50
    asn: 65530
    local-pref: 50
    neighbors:
      - 192.0.2.3
```
