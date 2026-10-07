# Announcements

This page describes the peer options that control which routes are exported (announced) to a peer.

## Restricting announcements by prefix

`dont-announce` and `only-announce` take a list of prefixes in BIRD prefix pattern syntax, so each entry may be a plain
prefix (`192.0.2.0/24`), a prefix with a length range (`192.0.2.0/24{24,32}`), or a prefix followed by `+` (the prefix
and all more specifics) or `-` (the prefix and all less specifics).

- `dont-announce` rejects matching routes before any other export policy is evaluated.
- `only-announce` rejects every route that doesn't match the list.

IPv4 and IPv6 prefixes can be mixed in the same list. Pathvector splits each list by address family and every BGP
channel only checks the prefixes of its own family, so a single peer with both IPv4 and IPv6 neighbors (or with
`mp-unicast-46`) can share one list:

```yaml
peers:
  Downstream:
    asn: 65510
    neighbors:
      - 192.0.2.10
      - 2001:db8::10
    only-announce:
      - 198.51.100.0/24
      - 2001:db8:1000::/36+
    dont-announce:
      - 198.51.100.128/25
      - 2001:db8:1fff::/48
```

If `only-announce` is set but contains no prefixes of an address family, nothing is announced in that address family
(for example, an `only-announce` list with only IPv4 prefixes means no IPv6 routes are announced to the peer).
`only-announce` cannot be combined with `announce-all`.
