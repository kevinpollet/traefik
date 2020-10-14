package gateway

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/mitchellh/hashstructure"
	ptypes "github.com/traefik/paerser/types"
	"github.com/traefik/traefik/v2/pkg/config/dynamic"
	"github.com/traefik/traefik/v2/pkg/job"
	"github.com/traefik/traefik/v2/pkg/log"
	"github.com/traefik/traefik/v2/pkg/provider"
	"github.com/traefik/traefik/v2/pkg/safe"
	"github.com/traefik/traefik/v2/pkg/tls"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/service-apis/apis/v1alpha1"
)

const (
	providerName = "kubernetesgateway"
)

// Provider holds configurations of the provider.
type Provider struct {
	Endpoint          string          `description:"Kubernetes server endpoint (required for external cluster client)." json:"endpoint,omitempty" toml:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	Token             string          `description:"Kubernetes bearer token (not needed for in-cluster client)." json:"token,omitempty" toml:"token,omitempty" yaml:"token,omitempty"`
	CertAuthFilePath  string          `description:"Kubernetes certificate authority file path (not needed for in-cluster client)." json:"certAuthFilePath,omitempty" toml:"certAuthFilePath,omitempty" yaml:"certAuthFilePath,omitempty"`
	Namespaces        []string        `description:"Kubernetes namespaces." json:"namespaces,omitempty" toml:"namespaces,omitempty" yaml:"namespaces,omitempty" export:"true"`
	LabelSelector     string          `description:"Kubernetes label selector to use." json:"labelSelector,omitempty" toml:"labelSelector,omitempty" yaml:"labelSelector,omitempty" export:"true"`
	ThrottleDuration  ptypes.Duration `description:"Ingress refresh throttle duration" json:"throttleDuration,omitempty" toml:"throttleDuration,omitempty" yaml:"throttleDuration,omitempty"`
	lastConfiguration safe.Safe
	EntryPoints       map[string]Entrypoint `json:"-" toml:"-" yaml:"-" label:"-" file:"-"`
}

// Entrypoint defines the available entry points.
type Entrypoint struct {
	Address        string
	HasHTTPTLSConf bool
}

func (p *Provider) newK8sClient(ctx context.Context) (*clientWrapper, error) {
	labelSel, err := labels.Parse(p.LabelSelector)
	if err != nil {
		return nil, fmt.Errorf("invalid label selector: %q", p.LabelSelector)
	}
	log.FromContext(ctx).Infof("label selector is: %q", labelSel)

	withEndpoint := ""
	if p.Endpoint != "" {
		withEndpoint = fmt.Sprintf(" with endpoint %s", p.Endpoint)
	}

	var client *clientWrapper
	switch {
	case os.Getenv("KUBERNETES_SERVICE_HOST") != "" && os.Getenv("KUBERNETES_SERVICE_PORT") != "":
		log.FromContext(ctx).Infof("Creating in-cluster Provider client%s", withEndpoint)
		client, err = newInClusterClient(p.Endpoint)
	case os.Getenv("KUBECONFIG") != "":
		log.FromContext(ctx).Infof("Creating cluster-external Provider client from KUBECONFIG %s", os.Getenv("KUBECONFIG"))
		client, err = newExternalClusterClientFromFile(os.Getenv("KUBECONFIG"))
	default:
		log.FromContext(ctx).Infof("Creating cluster-external Provider client%s", withEndpoint)
		client, err = newExternalClusterClient(p.Endpoint, p.Token, p.CertAuthFilePath)
	}

	if err == nil {
		client.labelSelector = labelSel
	}

	return client, err
}

// Init the provider.
func (p *Provider) Init() error {
	return nil
}

// Provide allows the k8s provider to provide configurations to traefik
// using the given configuration channel.
func (p *Provider) Provide(configurationChan chan<- dynamic.Message, pool *safe.Pool) error {
	ctxLog := log.With(context.Background(), log.Str(log.ProviderName, providerName))
	logger := log.FromContext(ctxLog)

	logger.Debugf("Using label selector: %q", p.LabelSelector)
	k8sClient, err := p.newK8sClient(ctxLog)
	if err != nil {
		return err
	}

	pool.GoCtx(func(ctxPool context.Context) {
		operation := func() error {
			eventsChan, err := k8sClient.WatchAll(p.Namespaces, ctxPool.Done())
			if err != nil {
				logger.Errorf("Error watching kubernetes events: %v", err)
				timer := time.NewTimer(1 * time.Second)
				select {
				case <-timer.C:
					return err
				case <-ctxPool.Done():
					return nil
				}
			}

			throttleDuration := time.Duration(p.ThrottleDuration)
			throttledChan := throttleEvents(ctxLog, throttleDuration, pool, eventsChan)
			if throttledChan != nil {
				eventsChan = throttledChan
			}

			for {
				select {
				case <-ctxPool.Done():
					return nil
				case event := <-eventsChan:
					// Note that event is the *first* event that came in during this throttling interval -- if we're hitting our throttle, we may have dropped events.
					// This is fine, because we don't treat different event types differently.
					// But if we do in the future, we'll need to track more information about the dropped events.
					conf := p.loadConfigurationFromGateway(ctxLog, k8sClient)

					confHash, err := hashstructure.Hash(conf, nil)
					switch {
					case err != nil:
						logger.Error("Unable to hash the configuration")
					case p.lastConfiguration.Get() == confHash:
						logger.Debugf("Skipping Kubernetes event kind %T", event)
					default:
						p.lastConfiguration.Set(confHash)
						configurationChan <- dynamic.Message{
							ProviderName:  providerName,
							Configuration: conf,
						}
					}

					// If we're throttling,
					// we sleep here for the throttle duration to enforce that we don't refresh faster than our throttle.
					// time.Sleep returns immediately if p.ThrottleDuration is 0 (no throttle).
					time.Sleep(throttleDuration)
				}
			}
		}

		notify := func(err error, time time.Duration) {
			logger.Errorf("Provider connection error: %v; retrying in %s", err, time)
		}
		err := backoff.RetryNotify(safe.OperationWithRecover(operation), backoff.WithContext(job.NewBackOff(backoff.NewExponentialBackOff()), ctxPool), notify)
		if err != nil {
			logger.Errorf("Cannot connect to Provider: %v", err)
		}
	})

	return nil
}

// TODO Handle errors and update resources statuses (gatewayClass, gateway).
func (p *Provider) loadConfigurationFromGateway(ctx context.Context, client Client) *dynamic.Configuration {
	tlsConfigs := make(map[string]*tls.CertAndStores)
	routers := make(map[string]*dynamic.Router)
	services := make(map[string]*dynamic.Service)

	conf := &dynamic.Configuration{
		UDP: &dynamic.UDPConfiguration{
			Routers:  map[string]*dynamic.UDPRouter{},
			Services: map[string]*dynamic.UDPService{},
		},
		TCP: &dynamic.TCPConfiguration{
			Routers:  map[string]*dynamic.TCPRouter{},
			Services: map[string]*dynamic.TCPService{},
		},
		HTTP: &dynamic.HTTPConfiguration{
			Routers:     map[string]*dynamic.Router{},
			Middlewares: map[string]*dynamic.Middleware{},
			Services:    map[string]*dynamic.Service{},
		},
		TLS: &dynamic.TLSConfiguration{},
	}

	logger := log.FromContext(log.With(ctx))

	allowedGateway := map[string]struct{}{}

	for _, gatewayClass := range client.GetGatewayClasses() {
		if gatewayClass.Spec.Controller == "traefik.io/gateway-controller" {
			allowedGateway[gatewayClass.Name] = struct{}{}
		}
	}

	for _, gateway := range client.GetGateways() {
		if _, ok := allowedGateway[gateway.Spec.GatewayClassName]; !ok {
			continue
		}

		for _, listener := range gateway.Spec.Listeners {
			ep, err := p.entryPointName(listener.Port, listener.Protocol)
			if err != nil {
				logger.Errorf("Cannot found entryPoint for Gateway %q : %v", gateway.Name, err)
				continue
			}

			if listener.Protocol == v1alpha1.HTTPSProtocolType || listener.Protocol == v1alpha1.TLSProtocolType {
				if listener.TLS == nil {
					logger.Errorf("No TLS configuration for Gateway %q listener port %d and protocol %q", gateway.Name, listener.Port, listener.Protocol)
					continue
				}

				if listener.TLS.CertificateRef.Name == "" {
					logger.Errorf("Empty TLS CertificateRef secret name")
					continue
				}

				if listener.TLS.CertificateRef.Kind != "Secret" && listener.TLS.CertificateRef.Group != "core" {
					logger.Debugf("Unsupported TLS CertificateRef group/kind : %v/%v", listener.TLS.CertificateRef.Group, listener.TLS.CertificateRef.Kind)
					continue
				}

				configKey := gateway.Namespace + "/" + listener.TLS.CertificateRef.Name
				if _, tlsExists := tlsConfigs[configKey]; !tlsExists {
					tlsConf, err := getTLS(client, listener.TLS.CertificateRef.Name, gateway.Namespace)
					if err != nil {
						logger.Errorf("Error while retrieving certificate: %v", err)
						continue
					}

					tlsConfigs[configKey] = tlsConf
				}
			}

			if listener.Protocol != v1alpha1.HTTPProtocolType && listener.Protocol != v1alpha1.HTTPSProtocolType {
				continue
			}

			if listener.Routes.Kind != "HTTPRoute" {
				continue
			}

			httpRoutes, exist, err := client.GetHTTPRoutes(gateway.Namespace, labels.SelectorFromSet(listener.Routes.Selector.MatchLabels))
			if err != nil {
				logger.Errorf("Cannot fetch HTTPRoutes for namespace %q and matchLabels %v", gateway.Namespace, listener.Routes.Selector.MatchLabels)
			}
			if !exist {
				logger.Errorf("No match for HTTPRoute with namespace %q and matchLabels %v", gateway.Namespace, listener.Routes.Selector.MatchLabels)
			}

			for _, httpRoute := range httpRoutes {
				if httpRoute == nil {
					continue
				}

				hostRule := hostRule(httpRoute.Spec)

				for _, routeRule := range httpRoute.Spec.Rules {
					router := dynamic.Router{
						Rule:        extractRule(routeRule, hostRule),
						EntryPoints: []string{ep},
					}

					if listener.TLS != nil {
						// TODO support let's encrypt
						router.TLS = &dynamic.RouterTLSConfig{}
					}

					routerKey, err := makeRouterKey(router.Rule, makeID(httpRoute.Namespace, httpRoute.Name))
					if err != nil {
						logger.Errorf("Skipping HTTPRoute %s: cannot make router's key with rule %s: %v", httpRoute.Name, router.Rule, err)
						continue
					}

					if routeRule.ForwardTo != nil {
						wrrService, subServices, err := loadServices(client, gateway.Namespace, routeRule.ForwardTo)
						if err != nil {
							logger.Errorf("Cannot load service from HTTPRoute %s/%s : %v", gateway.Namespace, httpRoute.Name, err)
							continue
						}

						for svcName, svc := range subServices {
							services[svcName] = svc
						}

						serviceName := provider.Normalize(routerKey + "-wrr")
						services[serviceName] = wrrService

						router.Service = serviceName
					}

					if router.Service != "" {
						routerKey = provider.Normalize(routerKey)
						routers[routerKey] = &router
					}
				}
			}
		}
	}

	if len(routers) > 0 {
		conf.HTTP.Routers = routers
	}

	if len(services) > 0 {
		conf.HTTP.Services = services
	}

	if len(tlsConfigs) > 0 {
		conf.TLS = &dynamic.TLSConfiguration{
			Certificates: getTLSConfig(tlsConfigs),
		}
	}

	return conf
}

func hostRule(httpRouteSpec v1alpha1.HTTPRouteSpec) string {
	if len(httpRouteSpec.Hostnames) > 0 {
		hostRule := ""
		for i, hostname := range httpRouteSpec.Hostnames {
			if i > 0 && len(hostname) > 0 {
				hostRule += "`, `"
			}
			hostRule += string(hostname)
		}

		if hostRule != "" {
			return "Host(`" + hostRule + "`)"
		}
	}

	return ""
}

func extractRule(routeRule v1alpha1.HTTPRouteRule, hostRule string) string {
	var rule string
	var matchesRules []string

	for _, match := range routeRule.Matches {
		var matchRules []string
		// TODO handle other path types
		if match.Path.Type == v1alpha1.PathMatchExact {
			matchRules = append(matchRules, "Path(`"+match.Path.Value+"`)")
		}

		if match.Path.Type == v1alpha1.PathMatchPrefix {
			matchRules = append(matchRules, "PathPrefix(`"+match.Path.Value+"`)")
		}

		// TODO handle other headers types
		if match.Headers != nil && match.Headers.Type == v1alpha1.HeaderMatchExact {
			var headerRules []string
			for headerName, headerValue := range match.Headers.Values {
				headerRules = append(headerRules, "Headers(`"+headerName+"`,`"+headerValue+"`)")
			}
			matchRules = append(matchRules, headerRules...)
		}

		matchesRules = append(matchesRules, strings.Join(matchRules, " && "))
	}

	// If no matches are specified, the default is a prefix
	// path match on "/", which has the effect of matching every
	// HTTP request.
	if len(routeRule.Matches) == 0 {
		matchesRules = append(matchesRules, "PathPrefix(`/`)")
	}

	if hostRule != "" {
		if len(matchesRules) == 0 {
			return hostRule
		}
		rule += hostRule + " && "
	}

	if len(matchesRules) == 1 {
		return rule + matchesRules[0]
	}

	if len(rule) == 0 {
		return strings.Join(matchesRules, " || ")
	}

	return rule + "(" + strings.Join(matchesRules, " || ") + ")"
}

func (p *Provider) entryPointName(port int32, protocol v1alpha1.ProtocolType) (string, error) {
	entryPointName := ""
	portStr := strconv.FormatInt(int64(port), 10)
	for name, entryPoint := range p.EntryPoints {
		if strings.HasSuffix(entryPoint.Address, ":"+portStr) {
			// if the protocol is HTTP the entryPoint must have no TLS conf
			if protocol != "HTTP" || !entryPoint.HasHTTPTLSConf {
				entryPointName = name
				break
			}
		}
	}

	if entryPointName == "" {
		return "", fmt.Errorf("no matching entryPoint for port %d and protocol %q", port, protocol)
	}

	return entryPointName, nil
}

func getServicePort(svc *corev1.Service, port int32) (*corev1.ServicePort, error) {
	if svc == nil {
		return nil, errors.New("service is not defined")
	}

	if port == 0 {
		return nil, errors.New("service port not defined")
	}

	hasValidPort := false
	for _, p := range svc.Spec.Ports {
		if p.Port == port {
			return &p, nil
		}

		if p.Port != 0 {
			hasValidPort = true
		}
	}

	if svc.Spec.Type != corev1.ServiceTypeExternalName {
		return nil, fmt.Errorf("service port not found: %d", port)
	}

	if hasValidPort {
		log.WithoutContext().
			Warningf("The port %d from HTTPRoute doesn't match with ports defined in the ExternalName service %s/%s.", port, svc.Namespace, svc.Name)
	}

	return &corev1.ServicePort{Port: port}, nil
}

func makeRouterKey(rule, name string) (string, error) {
	h := sha256.New()
	if _, err := h.Write([]byte(rule)); err != nil {
		return "", err
	}

	key := fmt.Sprintf("%s-%.10x", name, h.Sum(nil))

	return key, nil
}

func makeID(namespace, name string) string {
	if namespace == "" {
		return name
	}

	return namespace + "-" + name
}

func getTLS(k8sClient Client, secretName, namespace string) (*tls.CertAndStores, error) {
	secret, exists, err := k8sClient.GetSecret(namespace, secretName)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch secret %s/%s: %w", namespace, secretName, err)
	}
	if !exists {
		return nil, fmt.Errorf("secret %s/%s does not exist", namespace, secretName)
	}

	cert, key, err := getCertificateBlocks(secret, namespace, secretName)
	if err != nil {
		return nil, err
	}

	return &tls.CertAndStores{
		Certificate: tls.Certificate{
			CertFile: tls.FileOrContent(cert),
			KeyFile:  tls.FileOrContent(key),
		},
	}, nil
}

func getTLSConfig(tlsConfigs map[string]*tls.CertAndStores) []*tls.CertAndStores {
	var secretNames []string
	for secretName := range tlsConfigs {
		secretNames = append(secretNames, secretName)
	}
	sort.Strings(secretNames)

	var configs []*tls.CertAndStores
	for _, secretName := range secretNames {
		configs = append(configs, tlsConfigs[secretName])
	}

	return configs
}

func getCertificateBlocks(secret *corev1.Secret, namespace, secretName string) (string, string, error) {
	var missingEntries []string

	tlsCrtData, tlsCrtExists := secret.Data["tls.crt"]
	if !tlsCrtExists {
		missingEntries = append(missingEntries, "tls.crt")
	}

	tlsKeyData, tlsKeyExists := secret.Data["tls.key"]
	if !tlsKeyExists {
		missingEntries = append(missingEntries, "tls.key")
	}

	if len(missingEntries) > 0 {
		return "", "", fmt.Errorf("secret %s/%s is missing the following TLS data entries: %s",
			namespace, secretName, strings.Join(missingEntries, ", "))
	}

	cert := string(tlsCrtData)
	if cert == "" {
		missingEntries = append(missingEntries, "tls.crt")
	}

	key := string(tlsKeyData)
	if key == "" {
		missingEntries = append(missingEntries, "tls.key")
	}

	if len(missingEntries) > 0 {
		return "", "", fmt.Errorf("secret %s/%s contains the following empty TLS data entries: %s",
			namespace, secretName, strings.Join(missingEntries, ", "))
	}

	return cert, key, nil
}

// loadServices is generating a WRR service, even when there is only one target.
func loadServices(client Client, namespace string, targets []v1alpha1.HTTPRouteForwardTo) (*dynamic.Service, map[string]*dynamic.Service, error) {
	services := map[string]*dynamic.Service{}

	wrrSvc := &dynamic.Service{
		Weighted: &dynamic.WeightedRoundRobin{
			Services: []dynamic.WRRService{},
		},
	}

	for _, forwardTo := range targets {
		svc := dynamic.Service{
			LoadBalancer: &dynamic.ServersLoadBalancer{
				PassHostHeader: func(v bool) *bool { return &v }(true),
			},
		}

		// TODO Handle BackendRefs

		if forwardTo.ServiceName != nil {
			service, exists, err := client.GetService(namespace, *forwardTo.ServiceName)
			if err != nil {
				return nil, nil, err
			}

			if !exists {
				return nil, nil, errors.New("service not found")
			}

			if len(service.Spec.Ports) > 1 && forwardTo.Port == nil {
				// If the port is unspecified and the backend is a Service
				// object consisting of multiple port definitions, the route
				// must be dropped from the Gateway. The controller should
				// raise the "ResolvedRefs" condition on the Gateway with the
				// "DroppedRoutes" reason.  The gateway status for this route
				// should be updated with a condition that describes the error
				// more specifically.
				log.WithoutContext().Errorf("A multiple ports Kubernetes Service cannot be used if unspecified forwardTo.Port")
				continue
			}

			var portName string
			var portSpec corev1.ServicePort
			var match bool

			for _, p := range service.Spec.Ports {
				if forwardTo.Port == nil || p.TargetPort.IntVal == *forwardTo.Port {
					portName = p.Name
					portSpec = p
					match = true
					break
				}
			}

			if !match {
				return nil, nil, errors.New("service port not found")
			}

			endpoints, endpointsExists, endpointsErr := client.GetEndpoints(namespace, *forwardTo.ServiceName)
			if endpointsErr != nil {
				return nil, nil, endpointsErr
			}

			if !endpointsExists {
				return nil, nil, errors.New("endpoints not found")
			}

			if len(endpoints.Subsets) == 0 {
				return nil, nil, errors.New("subset not found")
			}

			var port int32
			for _, subset := range endpoints.Subsets {
				for _, p := range subset.Ports {
					if portName == p.Name {
						port = p.Port
						break
					}
				}

				if port == 0 {
					return nil, nil, errors.New("cannot define a port")
				}

				protocol := getProtocol(portSpec, portName)

				for _, addr := range subset.Addresses {
					svc.LoadBalancer.Servers = append(svc.LoadBalancer.Servers, dynamic.Server{
						URL: fmt.Sprintf("%s://%s:%d", protocol, addr.IP, port),
					})
				}
			}

			serviceName := provider.Normalize(makeID(service.Namespace, service.Name) + "-" + strconv.FormatInt(int64(port), 10))
			services[serviceName] = &svc

			weight := int(forwardTo.Weight)
			wrrSvc.Weighted.Services = append(wrrSvc.Weighted.Services, dynamic.WRRService{Name: serviceName, Weight: &weight})
		}
	}

	if len(services) == 0 {
		return nil, nil, errors.New("no service has been created")
	}

	return wrrSvc, services, nil
}

func getProtocol(portSpec corev1.ServicePort, portName string) string {
	protocol := "http"
	if portSpec.Port == 443 || strings.HasPrefix(portName, "https") {
		protocol = "https"
	}

	return protocol
}

func throttleEvents(ctx context.Context, throttleDuration time.Duration, pool *safe.Pool, eventsChan <-chan interface{}) chan interface{} {
	if throttleDuration == 0 {
		return nil
	}
	// Create a buffered channel to hold the pending event (if we're delaying processing the event due to throttling)
	eventsChanBuffered := make(chan interface{}, 1)

	// Run a goroutine that reads events from eventChan and does a non-blocking write to pendingEvent.
	// This guarantees that writing to eventChan will never block,
	// and that pendingEvent will have something in it if there's been an event since we read from that channel.
	pool.GoCtx(func(ctxPool context.Context) {
		for {
			select {
			case <-ctxPool.Done():
				return
			case nextEvent := <-eventsChan:
				select {
				case eventsChanBuffered <- nextEvent:
				default:
					// We already have an event in eventsChanBuffered, so we'll do a refresh as soon as our throttle allows us to.
					// It's fine to drop the event and keep whatever's in the buffer -- we don't do different things for different events
					log.FromContext(ctx).Debugf("Dropping event kind %T due to throttling", nextEvent)
				}
			}
		}
	})

	return eventsChanBuffered
}
