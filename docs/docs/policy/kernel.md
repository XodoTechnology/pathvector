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
