---
title: BGP Sessions
sidebar_position: 5
---

# BGP Sessions

This page covers peer options that change how BGP sessions and their next hops are set up. See the
[configuration reference](/docs/configuration#peers) for every option.

## Gateway mode

BIRD resolves a BGP route's next hop either *directly* (the next hop must be on a directly connected network) or
*recursively* (the next hop is looked up in the routing table, like an IGP next hop). BIRD uses direct mode for directly
connected eBGP neighbors and recursive mode for multihop and iBGP sessions.

The `gateway` peer option (`direct` or `recursive`) overrides this. A common case is an upstream reached over an IPv6
link-local address: the session can't use `multihop`, but the routes' next hops may still need to be resolved
recursively.

```yaml
peers:
  Upstream:
    asn: 64496
    neighbors:
      - fe80::1%eth0
    gateway: recursive
```

## Next hop cost

In recursive gateway mode (mainly multihop iBGP), BIRD uses the IGP metric of the path to the BGP next hop as a tie
breaker in best path selection. Sessions in direct gateway mode have no IGP metric, so all of them look equally close.

The `cost` peer option sets the distance to the next hop for routes from a session in direct gateway mode, for example
to prefer one directly connected iBGP neighbor over another when no IGP provides metrics:

```yaml
peers:
  Core A:
    asn: 65530
    direct: true
    cost: 10
    neighbors:
      - 192.0.2.2
  Core B:
    asn: 65530
    direct: true
    cost: 20
    neighbors:
      - 192.0.2.3
```

`cost` is a per-channel option in BIRD, which is why it can't be set with `session-global`.
