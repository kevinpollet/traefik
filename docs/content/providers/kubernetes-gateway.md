# Traefik & Kubernetes with Gateway API

The Kubernetes Gateway API, The Experimental Way.
{: .subtitle }

Gateway API is the evolution of Kubernetes APIs that relate to `Services`, e.g. `Ingress`.
The Gateway API project is part of Kubernetes, working under SIG-NETWORK.

The Kubernetes Gateway provider is a Traefik implementation of the [service-apis](https://github.com/kubernetes-sigs/service-apis)
specifications from the Kubernetes SIGs.   

This provider is proposed as an experimental feature.

!!!warning "Enabling The Experimental Kubernetes Gateway Provider"
    
    As long as this provider is in experimental stage, it needs to be activated in the experimental static configuration. 
    
    ```toml tab="File (TOML)"
    [experimental]
      kubernetesGateway = true
    
    [providers.kubernetesGateway]
    #...
    ```
    
    ```yaml tab="File (YAML)"
    experimental:
      kubernetesGateway: true
    
    providers:
      kubernetesGateway: {}
      #...
    ```
    
    ```bash tab="CLI"
    --experimental.kubernetesgateway=true --providers.kubernetesgateway=true #...
    ```

## Configuration Requirements

!!! tip "All Steps for a Successful Deployment"
  
    * Add/update the Kubernetes Gateway API [definitions](../reference/dynamic-configuration/kubernetes-gateway.md#definitions)
    * Add/update the [RBAC](https://kubernetes.io/docs/reference/access-authn-authz/rbac/) for the Traefik custom resources
    * Add all needed Kubernetes Gateway API [resources](../reference/dynamic-configuration/kubernetes-gateway.md#resources)
 
??? example "Kubernetes Gateway Provider Basic Example"

    ```yaml tab="Gateway API"
    ---
    kind: GatewayClass
    apiVersion: networking.x-k8s.io/v1alpha1
    metadata:
      name: my-gateway-class
    spec:
      controller: traefik.io/gateway-controller
    
    ---
    kind: Gateway
    apiVersion: networking.x-k8s.io/v1alpha1
    metadata:
      name: my-gateway
      namespace: default
    spec:
      gatewayClassName: my-gateway-class
      listeners:
        - protocol: HTTP
          port: 80
          routes:
            kind: HTTPRoute
            routeNamespaces:
              from: Same
            routeSelector:
              matchLabels:
                app: foo
    
    ---
    kind: HTTPRoute
    apiVersion: networking.x-k8s.io/v1alpha1
    metadata:
      name: http-app-1
      namespace: default
      labels:
        app: foo
    spec:
      hostnames:
        - "whoami"
      rules:
        - matches:
            - path:
                type: Exact
                value: /bar
          forwardTo:
            - serviceName: whoami
              weight: 1
    ```

    ```yaml tab="Whoami Service"
    ---
    kind: Deployment
    apiVersion: apps/v1
    metadata:
      name: whoami
      namespace: default
      labels:
        app: traefik
        name: whoami
    
    spec:
      replicas: 2
      selector:
        matchLabels:
          app: traefik
          task: whoami
      template:
        metadata:
          labels:
            app: traefik
            task: whoami
        spec:
          containers:
            - name: whoami
              image: traefik/whoami
    
    ---
    apiVersion: v1
    kind: Service
    metadata:
      name: whoami
      namespace: default
    
    spec:
      ports:
        - protocol: TCP
          port: 80
      selector:
        app: traefik
        task: whoami
    ```
    
    ```yaml tab="Traefik Service"
    apiVersion: v1
    kind: ServiceAccount
    metadata:
      name: traefik-controller
    
    ---
    kind: Deployment
    apiVersion: apps/v1
    metadata:
      name: traefik
      labels:
        app: traefik
    
    spec:
      replicas: 1
      selector:
        matchLabels:
          app: traefik
      template:
        metadata:
          labels:
            app: traefik
        spec:
          serviceAccountName: traefik-controller
          containers:
            - name: traefik
              image: <traefik experimental image>
              args:
                - --log.level=DEBUG
                - --entrypoints.web.address=:80
                - --experimental.kubernetesgateway
                - --providers.kubernetesgateway
              ports:
                - name: web
                  containerPort: 80
    
    ---
    apiVersion: v1
    kind: Service
    metadata:
      name: traefik
    spec:
      selector:
        app: traefik
      ports:
        - protocol: TCP
          port: 80
          targetPort: 80
          name: web
      type: LoadBalancer
    ```
    
    ```yaml tab="Gateway API CRDs"
    # All resources definition must be declared
    --8<-- "content/reference/dynamic-configuration/networking.x-k8s.io_gatewayclasses.yaml"
    --8<-- "content/reference/dynamic-configuration/networking.x-k8s.io_gateways.yaml"
    --8<-- "content/reference/dynamic-configuration/networking.x-k8s.io_httproutes.yaml"
    ```

    ```yaml tab="RBAC"
    --8<-- "content/reference/dynamic-configuration/kubernetes-gateway-rbac.yml"
    ```
    
## Resource Configuration

When using Kubernetes Gateway API as a provider,
Traefik uses Kubernetes 
[Custom Resource Definition](https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/custom-resources/)
to retrieve its routing configuration.

All concepts can be found in the official API concepts [documentation](https://kubernetes-sigs.github.io/service-apis/concepts/).
The implemented resources by Traefik :

* `GatewayClass` defines a set of Gateways that share a common configuration and behaviour.
* `Gateway` describes how traffic can be translated to Services within the cluster.
* `HTTPRoute` define HTTP rules for mapping requests from a Gateway to Kubernetes Services.

## Provider Configuration 

### `endpoint`

_Optional, Default=empty_

```toml tab="File (TOML)"
[experimental]
  kubernetesGateway = true

[providers.kubernetesGateway]
  endpoint = "http://localhost:8080"
  # ...
```

```yaml tab="File (YAML)"
experimental:
  kubernetesGateway: true

providers:
  kubernetesGateway:
    endpoint: "http://localhost:8080"
    # ...
```

```bash tab="CLI"
--experimental.kubernetesgateway=true --providers.kubernetesgateway.endpoint=http://localhost:8080
```

The Kubernetes server endpoint as URL.

When deployed into Kubernetes, Traefik will read the environment variables `KUBERNETES_SERVICE_HOST` and `KUBERNETES_SERVICE_PORT` or `KUBECONFIG` to construct the endpoint.

The access token will be looked up in `/var/run/secrets/kubernetes.io/serviceaccount/token` and the SSL CA certificate in `/var/run/secrets/kubernetes.io/serviceaccount/ca.crt`.
Both are provided mounted automatically when deployed inside Kubernetes.

The endpoint may be specified to override the environment variable values inside a cluster.

When the environment variables are not found, Traefik will try to connect to the Kubernetes API server with an external-cluster client.
In this case, the endpoint is required.
Specifically, it may be set to the URL used by `kubectl proxy` to connect to a Kubernetes cluster using the granted authentication and authorization of the associated kubeconfig.

### `token`

_Optional, Default=empty_

```toml tab="File (TOML)"
[providers.kubernetesGateway]
  token = "mytoken"
  # ...
```

```yaml tab="File (YAML)"
providers:
  kubernetesGateway:
    token = "mytoken"
    # ...
```

```bash tab="CLI"
--providers.kubernetesgateway.token=mytoken
```

Bearer token used for the Kubernetes client configuration.

### `certAuthFilePath`

_Optional, Default=empty_

```toml tab="File (TOML)"
[providers.kubernetesGateway]
  certAuthFilePath = "/my/ca.crt"
  # ...
```

```yaml tab="File (YAML)"
providers:
  kubernetesGateway:
    certAuthFilePath: "/my/ca.crt"
    # ...
```

```bash tab="CLI"
--providers.kubernetesgateway.certauthfilepath=/my/ca.crt
```

Path to the certificate authority file.
Used for the Kubernetes client configuration.

### `namespaces`

_Optional, Default: all namespaces (empty array)_

```toml tab="File (TOML)"
[providers.kubernetesGateway]
  namespaces = ["default", "production"]
  # ...
```

```yaml tab="File (YAML)"
providers:
  kubernetesGateway:
    namespaces:
    - "default"
    - "production"
    # ...
```

```bash tab="CLI"
--providers.kubernetesgateway.namespaces=default,production
```

Array of namespaces to watch.

### `labelselector`

_Optional,Default: empty (process all resources)_

```toml tab="File (TOML)"
[providers.kubernetesGateway]
  labelselector = "app=traefik"
  # ...
```

```yaml tab="File (YAML)"
providers:
  kubernetesGateway:
    labelselector: "app=traefik"
    # ...
```

```bash tab="CLI"
--providers.kubernetesgateway.labelselector="app=traefik"
```

By default, Traefik processes all resource objects in the configured namespaces.
A label selector can be defined to filter on specific GatewayClass objects only.

See [label-selectors](https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#label-selectors) for details.

### `throttleDuration`

_Optional, Default: 0 (no throttling)_

```toml tab="File (TOML)"
[providers.kubernetesGateway]
  throttleDuration = "10s"
  # ...
```

```yaml tab="File (YAML)"
providers:
  kubernetesGateway:
    throttleDuration: "10s"
    # ...
```

```bash tab="CLI"
--providers.kubernetesgateway.throttleDuration=10s
```
