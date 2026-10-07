---
title: CLI Usage
sidebar_position: 6
---
# Usage
```
Pathvector is a declarative edge routing platform that automates route optimization and control plane configuration with secure and repeatable routing policy.

Usage:
  pathvector [command]

Available Commands:
  birdsh      Lightweight BIRD shell
  completion  Generate the autocompletion script for the specified shell
  config      Export configuration, optionally sanitized with logknife
  dump        Dump configuration
  generate    Generate router configuration
  help        Help about any command
  match       Find common IXPs for a given ASN
  optimizer   Start optimization daemon
  status      Show protocol status
  version     Show version information
  yang        Print YANG model of the configuration

Flags:
  -c, --config string   YAML configuration file (default "/etc/pathvector.yml")
  -d, --dry-run         Don't modify configuration
  -h, --help            help for pathvector
      --lock string     Lock file (check disabled if empty)
  -n, --no-configure    Don't configure BIRD
  -t, --trace           Show trace log messages
  -v, --verbose         Show verbose log messages

Use "pathvector [command] --help" for more information about a command.
```

## version

`pathvector version` prints the Pathvector build information and the version of the running BIRD daemon. The BIRD version is taken from the greeting BIRD sends when a client connects (e.g. `BIRD 2.14 ready.`). Some BIRD builds omit the version from the greeting (`BIRD ready.`), in which case Pathvector queries `show status` instead. If the version still can't be determined, it is reported as `unknown`. Pathvector logs a warning if the running BIRD is older than the minimum supported version (2.0.7).
