# IRR

IRR filtering uses [bgpq4](https://github.com/bgp/bgpq4) to generate sets of prefixes and ASNs.

## Global configuration

`irr-server` sets the IRR server address

`bgpq-args` adds additional arguments to pass to `bgpq4` (for example to limit IRR sources with `-S RIPE`)

### IRR sources

An as-set may be prefixed with the IRR database (source) that it should be looked up in, using the `SOURCE::AS-SET` syntax, for example `RIPE::AS-EXAMPLE`. This syntax is commonly found in PeeringDB `irr_as_set` values, so it is also used by `auto-as-set`.

How the source prefix is handled depends on whether `bgpq-args` already restricts the sources with a `-S` flag:

- If `bgpq-args` does **not** contain `-S`, the as-set's source is passed to bgpq4, so `RIPE::AS-EXAMPLE` is queried as `bgpq4 ... -S RIPE AS-EXAMPLE`.
- If `bgpq-args` **does** contain `-S`, your source list takes precedence: the `SOURCE::` prefix is stripped and no extra `-S` flag is added, so `RIPE::AS-EXAMPLE` is queried as `bgpq4 -S RPKI,RADB,RIPE ... AS-EXAMPLE`. (bgpq4 only honours the last `-S` flag, so adding the as-set's source would silently replace your list and can return an empty result.)

This applies to all bgpq4 queries: prefix sets (`filter-irr`) and AS set members (`auto-as-set-members`).

```yaml
bgpq-args: -S RPKI,AFRINIC,ARIN,APNIC,LACNIC,RADB,RIPE
peers:
  Example:
    asn: 65530
    auto-as-set: true # PeeringDB returns RIPE::AS-EXAMPLE, queried as AS-EXAMPLE using the sources above
    filter-irr: true
```

## Peer configuration

Enable `filter-irr` to enable IRR filtering.

Enable `filter-as-members` to reject routes that aren't originated from an ASN within the peer's `as-members` list.
Enable `auto-as-set-members` to retrieve that list automatically from their PeeringDB IRR object.
