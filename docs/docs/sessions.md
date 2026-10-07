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
