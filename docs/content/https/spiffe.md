---
title: "Traefik SPIFFE Documentation"
description: "Learn how to configure Traefik to use SPIFFE to do mTLS. Read the technical documentation."
---

# SPIFFE

Secure the backend connection with SPIFFE.
{: .subtitle }

[SPIFFE](https://spiffe.io/docs/latest/spiffe-about/overview/) (Secure Production Identity Framework For Everyone), 
provides a secure identity in the form of a specially crafted X.509 certificate, 
to every workload in an environment.

Traefik is able to connect to the Workload API to obtain a SPIFFE ID which will be used to secure the communication with SPIFFE enabled backends.

## Workload API

To connect to the SPIFFE [Workload API](https://spiffe.io/docs/latest/spiffe-about/spiffe-concepts/#spiffe-workload-api),
its address needs to be configured in the static configuration.

!!! info "Enable a certificate resolver"

    Enabling SPIFFE does not imply that connections are going to use it automatically.
    Each router or entrypoint that is meant to use the resolver must explicitly [reference](../routing/routers/index.md#certresolver) it.

```yaml tab="File (YAML)"
## Static configuration
spiffe:
    workloadAPIAddr: localhost
```

```toml tab="File (TOML)"
[spiffe]
    workloadAPIAddr: localhost
```

```bash tab="CLI"
--spiffe.workloadAPIAddr=localhost
```
