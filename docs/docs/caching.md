---
title: Caching
sidebar_position: 9
---

Pathvector relies on external datasources to generate configuration, such as PeeringDB, IRR databases, and the RPKI. There are various mechanisms to cache this data to decrease latency and reduce load on these external services.

## RPKI

Networks should already be running their own RTR (RPKI to Router) server such as [stayrtr](https://github.com/bgp/stayrtr) or [rtrtr](https://github.com/NLnetLabs/rtrtr).

## IRR

## PeeringDB

Pathvector has an internal PeeringDB cache that stores PeeringDB objects *for the duration of a single `pathvector generate` run*. This does not cache for longer than a single command invocation.

The in-memory cache is controlled by the global [`peeringdb-cache`](https://pathvector.io/docs/configuration/#peeringdb-cache) option (enabled by default). When enabled, peers that share an ASN only query PeeringDB once per run. `peeringdb-cache` is a global option, so it must be set at the top level of the config file, not under a peer or template.

### Disabling PeeringDB queries for a peer

Setting `peeringdb-cache: false` doesn't stop PeeringDB queries, it only disables the in-memory cache. Pathvector queries PeeringDB for a peer only when [`auto-import-limits`](https://pathvector.io/docs/configuration/#auto-import-limits) or [`auto-as-set`](https://pathvector.io/docs/configuration/#auto-as-set) is enabled for that peer (directly, or through a template). To stop querying PeeringDB for a single peer, disable both options and set the values manually:

```yaml
peers:
  Example:
    asn: 65510
    neighbors:
      - 203.0.113.12
    auto-import-limits: false
    auto-as-set: false
    import-limit4: 100
    import-limit6: 50
    as-set: AS-EXAMPLE
```

Separately from per-peer queries, Pathvector makes one global PeeringDB query per run for the list of networks that should never be reachable via route servers when a peer has [`filter-never-via-route-servers`](https://pathvector.io/docs/configuration/#filter-never-via-route-servers) set.

### PeeringDB Local Cache

To cache PeeringDB data persistently, you can set the global [`peeringdb-url`](https://pathvector.io/docs/configuration/#peeringdb-url) option to a local [PeeringDB cache server](https://github.com/natesales/peeringdb-cache).
