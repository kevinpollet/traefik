# Traefik & Kubernetes

The Kubernetes Gateway API, The Experimental Way.
{: .subtitle }

## Configuration Examples

??? example "Configuring Kubernetes Gateway provider and Deploying/Exposing Services"

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
      listeners:  # Use GatewayClass defaults for listener definition.
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


## Routing Configuration

### Custom Resource Definition (CRD)

* You can find an exhaustive list, of the custom resources and their attributes in
[the reference page](../../reference/dynamic-configuration/kubernetes-gateway.md) or in the Kubernetes Sigs `Service APIs` [repository](https://github.com/kubernetes-sigs/service-apis/).
* Validate that [the prerequisites](../../providers/kubernetes-gateway.md#configuration-requirements) are fulfilled before using the Traefik Kubernetes Gateway Provider.
    
You can find an excerpt of the supported Kubernetes Gateway API resources in the table below:

| Kind                               | Purpose                                                                   | Concept Behind                                                                        |
|------------------------------------|---------------------------------------------------------------------------|---------------------------------------------------------------------------------------|
| [GatewayClass](#kind-gatewayclass) | Defines a set of Gateways that share a common configuration and behaviour | [GatewayClass](https://kubernetes-sigs.github.io/service-apis/concepts/#gatewayclass) |
| [Gateway](#kind-gateway)           | describes how traffic can be translated to Services within the cluster    | [Gateway](https://kubernetes-sigs.github.io/service-apis/concepts/#gateway)           |
| [HTTPRoute](#kind-httproute)       | HTTP rules for mapping requests from a Gateway to Kubernetes Services     | [Route](https://kubernetes-sigs.github.io/service-apis/concepts/#httptcpfooroute)     |

### Kind: `GatewayClass`

`GatewayClass` (source code) is cluster-scoped resource defined by the infrastructure provider. This resource represents a class of Gateways that can be instantiated.
More details on the GatewayClass [official documentation](https://kubernetes-sigs.github.io/service-apis/concepts/#gatewayclass_1).

The `GatewayClass` should be declared by the infrastructure provider, otherwise please register the `GatewayClass`
[definition](../../reference/dynamic-configuration/kubernetes-gateway.md#definitions) in the Kubernetes cluster before 
creating `GatewayClass` objects.

!!! info "Declaring GatewayClass"

    ```yaml
    kind: GatewayClass
    apiVersion: networking.x-k8s.io/v1alpha1
    metadata:
      name: my-gateway-class
    spec:
      # Controller is a domain/path string that indicates
      # the controller that is managing Gateways of this class.
      controller: traefik.io/gateway-controller
    ```

### Kind: `Gateway`

`Gateway` A Gateway is 1:1 with the life cycle of the configuration of infrastructure. When a user creates a Gateway, 
some load balancing infrastructure is provisioned or configured by the GatewayClass controller. 
More details on the Gateway [official documentation](https://kubernetes-sigs.github.io/service-apis/concepts/#gateway_1).

Register the `Gateway` [definition](../../reference/dynamic-configuration/kubernetes-gateway.md#definitions) in the
Kubernetes cluster before creating `Gateway` objects.

!!! info "Declaring Gateway"

    ```yaml
    kind: Gateway
    apiVersion: networking.x-k8s.io/v1alpha1
    metadata:
      name: my-gateway
      namespace: default
    spec:
      gatewayClassName: my-gateway-class      # [1]
      listeners:                     # [2]
        - protocol: HTTPS            # [3] 
          port: 443                  # [4]
          tls:                       # [5]
            certificateRef:          # [6]
              name: supersecret
              kind: Secret
              group: core
          routes:                    # [7]
            kind: HTTPRoute          # [8]
            routeSelector:           # [9]
              matchLabels:
                app: foo
    ```

| Ref | Attribute          | Purpose                                                                                                                                |
|-----|--------------------|----------------------------------------------------------------------------------------------------------------------------------------|
| [1] | `gatewayClassName` | GatewayClassName used for this Gateway. This is the name of a GatewayClass resource.                                                   |
| [2] | `listeners`        | Listeners define logical endpoints that are bound on this Gateway's addresses. At least one Listener MUST be specified.                |
| [3] | `protocol`         | Protocol specifies the network protocol this listener expects to receive (only HTTP and HTTPS are implemented).                        |
| [4] | `port`             | Port is the network port.                                                                                                              |
| [5] | `tls`              | TLS is the TLS configuration for the Listener. This field is required if the Protocol field is "HTTPS" or "TLS" and ignored otherwise. |
| [6] | `CertificateRef`   | CertificateRef is the reference to Kubernetes object that contains a TLS certificate and private key.                                  |
| [7] | `Routes`           | Routes specifies a schema for associating routes with the Listener using selectors.                                                    |
| [8] | `Kind`             | Kind is kind of the referent                                                                                                           |
| [9] | `RouteSelector`    | RouteSelector specifies a set of route labels used for selecting  routes to associate with the Gateway.                                |

### Kind: `HTTPRoute`

`HTTPRoute`  define HTTP rules for mapping requests from a `Gateway` to Kubernetes Services. 

Register the `HTTPRoute` [definition](../../reference/dynamic-configuration/kubernetes-gateway.md#definitions) in the
Kubernetes cluster before creating `HTTPRoute` objects.

!!! info "Declaring HTTPRoute"

    ```yaml
    kind: HTTPRoute
    apiVersion: networking.x-k8s.io/v1alpha1
    metadata:
      name: http-app-1
      namespace: default
      labels:                       # [1]
        app: foo
    spec:
      hostnames:                    # [2]
        - "whoami"
      rules:                        # [3]
        - matches:                  # [4]
            - path:                 # [5]
                type: Exact         # [6]
                value: /bar         # [7]
            - headers:              # [8]
                type: Exact         # [9]
                values:             # [10]
                  - foo: bar
          forwardTo:                # [11]
            - serviceName: whoami   # [12]
              weight: 1             # [13]
    ```

| Ref  | Attribute     | Purpose                                                                                                                                                                                      |
|------|---------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| [1]  | `labels`      | Used to do the matching with the `Gateway` labelselector                                                                                                                                     |
| [2]  | `hostnames`   | `hostnames` defines a set of hostname that should match against the HTTP Host header to select a HTTPRoute to process the request.                                                           |
| [3]  | `rules`       | `rules` are a list of HTTP matchers, filters and actions.                                                                                                                                    |
| [4]  | `matches`     | `matches` define conditions used for matching the rule against incoming HTTP requests. Each match is independent, i.e. this rule will be matched if **any** one of the matches is satisfied. |
| [5]  | `path`        | `path` specifies a HTTP request path matcher. If this field is not specified, a default prefix match on the "/" path is provided.                                                            |
| [6]  | `type`        | `type` specifies how to match against the path Value (supported types: `Exact`, `Prefix`)                                                                                                    |
| [7]  | `value`       | `value` specifies the value of the HTTP path to match against                                                                                                                                |
| [8]  | `headers`     | `headers` describes how to select a HTTP route by matching HTTP request headers                                                                                                              |
| [9]  | `type`        | `type` specifies how to match a HTTP request header against the `values` (supported types: `Exact`)                                                                                          |
| [10] | `values`      | `values` is a map of HTTP Headers to be matched. It MUST contain at least one entry.                                                                                                         |
| [11] | `forwardTo`   | `forwardTo` defines the upstream target(s) where the request should be sent                                                                                                                  |
| [12] | `serviceName` | `serviceName` defines the name of the referent service                                                                                                                                       |
| [13] | `weight`      | `weight` specifies the proportion of traffic forwarded to a targetRef, computed as weight/(sum of all weights in targetRefs)                                                                 |