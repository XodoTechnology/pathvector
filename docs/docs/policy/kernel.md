# Kernel

Pathvector configures two BIRD kernel protocols that synchronise BIRD's routing tables with the operating system's
routing tables:

| BIRD protocol | Address family |
|---------------|----------------|
| `kernel4`     | IPv4           |
| `kernel6`     | IPv6           |

Use these names with `birdc`, for example `birdc show route export kernel4`. (Older versions left the protocols
unnamed, so BIRD called them `kernel1` and `kernel2`.)

Kernel options live under the global `kernel` key, see [the configuration reference](/docs/configuration#kernel):

```yaml
kernel:
  table: 100              # kernel table to sync with
  scan-time: 10           # seconds between kernel table scans
  learn: false            # learn routes from the kernel into BIRD
  export: true            # export routes to the kernel
  reject-connected: false # don't export connected (RTS_DEVICE) routes
  accept4: []             # BIRD protocols to always export to the kernel (IPv4)
  reject4: []             # BIRD protocols to never export to the kernel (IPv4)
```

## Static routes

`kernel.statics` maps a prefix to a next hop (optionally with an interface, `fe80::1%eth0`). Static routes are placed
in their own BIRD protocols, `statics4` and `statics6`, separate from the locally originated `prefixes` (`static4` /
`static6`), and are always exported to the kernel when `kernel.export` is enabled, including when `srd-communities` or
`source4`/`source6` are configured.

```yaml
kernel:
  statics:
    "198.51.100.0/24": 203.0.113.1
    "2001:db8:5::/48": "fe80::1%eth0"
```

## Selective route download (SRD)

`kernel.srd-communities` limits which BGP routes are installed in the kernel: if the list is not empty, only routes
carrying one of these communities (and `kernel.statics`) are exported to the kernel. This keeps the kernel FIB small
when BIRD holds full tables.

```yaml
kernel:
  srd-communities:
    - 65530:100
```
