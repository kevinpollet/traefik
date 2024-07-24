package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/traefik/traefik/v3/pkg/config/dynamic"
	"github.com/traefik/traefik/v3/pkg/provider"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ktypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	gatev1 "sigs.k8s.io/gateway-api/apis/v1"
)

func (p *Provider) loadGRPCRoutes(ctx context.Context, gatewayListeners []gatewayListener, conf *dynamic.Configuration) {
	routes, err := p.client.ListGRPCRoutes()
	if err != nil {
		log.Ctx(ctx).Error().Err(err).Msg("Unable to list GRPCRoutes")
		return
	}

	for _, route := range routes {
		logger := log.Ctx(ctx).With().
			Str("grpc_route", route.Name).
			Str("namespace", route.Namespace).
			Logger()

		var parentStatuses []gatev1.RouteParentStatus
		for _, parentRef := range route.Spec.ParentRefs {
			parentStatus := &gatev1.RouteParentStatus{
				ParentRef:      parentRef,
				ControllerName: controllerName,
				Conditions: []metav1.Condition{
					{
						Type:               string(gatev1.RouteConditionAccepted),
						Status:             metav1.ConditionFalse,
						ObservedGeneration: route.Generation,
						LastTransitionTime: metav1.Now(),
						Reason:             string(gatev1.RouteReasonNoMatchingParent),
					},
				},
			}

			for _, listener := range gatewayListeners {
				if !matchListener(listener, route.Namespace, parentRef) {
					continue
				}

				accepted := true
				if !allowRoute(listener, route.Namespace, kindGRPCRoute) {
					parentStatus.Conditions = updateRouteConditionAccepted(parentStatus.Conditions, string(gatev1.RouteReasonNotAllowedByListeners))
					accepted = false
				}
				hostnames, ok := findMatchingHostnames(listener.Hostname, route.Spec.Hostnames)
				if !ok {
					parentStatus.Conditions = updateRouteConditionAccepted(parentStatus.Conditions, string(gatev1.RouteReasonNoMatchingListenerHostname))
					accepted = false
				}

				if accepted {
					// Gateway listener should have AttachedRoutes set even when Gateway has unresolved refs.
					listener.Status.AttachedRoutes++
					// Only consider the route attached if the listener is in an "attached" state.
					if listener.Attached {
						parentStatus.Conditions = updateRouteConditionAccepted(parentStatus.Conditions, string(gatev1.RouteReasonAccepted))
					}
				}

				routeConf, resolveRefCondition := p.loadGRPCRoute(logger.WithContext(ctx), listener, route, hostnames)
				if accepted && listener.Attached {
					mergeHTTPConfiguration(routeConf, conf)
				}

				parentStatus.Conditions = upsertRouteConditionResolvedRefs(parentStatus.Conditions, resolveRefCondition)
			}

			parentStatuses = append(parentStatuses, *parentStatus)
		}

		status := gatev1.GRPCRouteStatus{
			RouteStatus: gatev1.RouteStatus{
				Parents: parentStatuses,
			},
		}
		if err := p.client.UpdateGRPCRouteStatus(ctx, ktypes.NamespacedName{Namespace: route.Namespace, Name: route.Name}, status); err != nil {
			logger.Warn().
				Err(err).
				Msg("Unable to update GRPCRoute status")
		}
	}
}

func (p *Provider) loadGRPCRoute(ctx context.Context, listener gatewayListener, route *gatev1.GRPCRoute, hostnames []gatev1.Hostname) (*dynamic.Configuration, metav1.Condition) {
	conf := &dynamic.Configuration{
		HTTP: &dynamic.HTTPConfiguration{
			Routers:           make(map[string]*dynamic.Router),
			Middlewares:       make(map[string]*dynamic.Middleware),
			Services:          make(map[string]*dynamic.Service),
			ServersTransports: make(map[string]*dynamic.ServersTransport),
		},
	}

	condition := metav1.Condition{
		Type:               string(gatev1.RouteConditionResolvedRefs),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: route.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             string(gatev1.RouteConditionResolvedRefs),
	}

	// FIXME KEep it?
	errWrr := dynamic.WeightedRoundRobin{
		Services: []dynamic.WRRService{
			{
				Name:   "invalid-httproute-filter",
				Status: ptr.To(500),
				Weight: ptr.To(1),
			},
		},
	}

	for ri, routeRule := range route.Spec.Rules {
		// Adding the gateway desc and the entryPoint desc prevents overlapping of routers build from the same routes.
		routeKey := provider.Normalize(fmt.Sprintf("%s-%s-%s-%s-%d", route.Namespace, route.Name, listener.GWName, listener.EPName, ri))

		for _, match := range routeRule.Matches {
			rule := buildGRPCMatchRule(hostnames, match)

			router := dynamic.Router{
				RuleSyntax:  "v3",
				Rule:        rule,
				EntryPoints: []string{listener.EPName},
			}
			if listener.Protocol == gatev1.HTTPSProtocolType {
				router.TLS = &dynamic.RouterTLSConfig{}
			}

			var err error
			routerName := makeRouterName(rule, routeKey)
			router.Middlewares, err = p.loadGRPCMiddlewares(conf, route.Namespace, routerName, routeRule.Filters)
			switch {
			case err != nil:
				log.Ctx(ctx).Error().Err(err).Msg("Unable to load GRPC route filters")

				// FIXME return a 500 here?
				errWrrName := routerName + "-err-wrr"
				conf.HTTP.Services[errWrrName] = &dynamic.Service{Weighted: &errWrr}
				router.Service = errWrrName

			default:
				var serviceCondition *metav1.Condition
				router.Service, serviceCondition = p.loadGRPCService(conf, routeKey, routeRule, route)
				if serviceCondition != nil {
					condition = *serviceCondition
				}
			}

			conf.HTTP.Routers[routerName] = &router
		}
	}

	return conf, condition
}

// FIXME do not support internal services
func (p *Provider) loadGRPCService(conf *dynamic.Configuration, routeKey string, routeRule gatev1.GRPCRouteRule, route *gatev1.GRPCRoute) (string, *metav1.Condition) {
	name := routeKey + "-wrr"
	if _, ok := conf.HTTP.Services[name]; ok {
		return name, nil
	}

	var wrr dynamic.WeightedRoundRobin
	var condition *metav1.Condition
	for _, backendRef := range routeRule.BackendRefs {
		svcName, svc, errCondition := p.loadGRPCBackendRef(route, backendRef)
		weight := ptr.To(int(ptr.Deref(backendRef.Weight, 1)))
		if errCondition != nil {
			condition = errCondition
			wrr.Services = append(wrr.Services, dynamic.WRRService{
				Name:   svcName,
				Status: ptr.To(500),
				Weight: weight,
			})
			continue
		}

		if svc != nil {
			conf.HTTP.Services[svcName] = svc
		}

		wrr.Services = append(wrr.Services, dynamic.WRRService{
			Name:   svcName,
			Weight: weight,
		})
	}

	conf.HTTP.Services[name] = &dynamic.Service{Weighted: &wrr}
	return name, condition
}

// loadGRPCBackendRef returns a dynamic.Service config corresponding to the given gatev1.GRPCBackendRef.
// Note that the returned dynamic.Service config can be nil (for cross-provider, internal services, and backendFunc).
func (p *Provider) loadGRPCBackendRef(route *gatev1.GRPCRoute, backendRef gatev1.GRPCBackendRef) (string, *dynamic.Service, *metav1.Condition) {
	kind := ptr.Deref(backendRef.Kind, "Service")

	group := groupCore
	if backendRef.Group != nil && *backendRef.Group != "" {
		group = string(*backendRef.Group)
	}

	namespace := route.Namespace
	if backendRef.Namespace != nil && *backendRef.Namespace != "" {
		namespace = string(*backendRef.Namespace)
	}

	serviceName := provider.Normalize(namespace + "-" + string(backendRef.Name))

	if err := p.isReferenceGranted(groupGateway, kindGRPCRoute, route.Namespace, group, string(kind), string(backendRef.Name), namespace); err != nil {
		return serviceName, nil, &metav1.Condition{
			Type:               string(gatev1.RouteConditionResolvedRefs),
			Status:             metav1.ConditionFalse,
			ObservedGeneration: route.Generation,
			LastTransitionTime: metav1.Now(),
			Reason:             string(gatev1.RouteReasonRefNotPermitted),
			Message:            fmt.Sprintf("Cannot load GRPCBackendRef %s/%s/%s/%s: %s", group, kind, namespace, backendRef.Name, err),
		}
	}

	if group != groupCore || kind != "Service" {
		return serviceName, nil, &metav1.Condition{
			Type:               string(gatev1.RouteConditionResolvedRefs),
			Status:             metav1.ConditionFalse,
			ObservedGeneration: route.Generation,
			LastTransitionTime: metav1.Now(),
			Reason:             string(gatev1.RouteReasonInvalidKind),
			Message:            fmt.Sprintf("Cannot load GRPCBackendRef %s/%s/%s/%s: only Kubernetes services are supported", group, kind, namespace, backendRef.Name),
		}
	}

	port := ptr.Deref(backendRef.Port, gatev1.PortNumber(0))
	if port == 0 {
		return serviceName, nil, &metav1.Condition{
			Type:               string(gatev1.RouteConditionResolvedRefs),
			Status:             metav1.ConditionFalse,
			ObservedGeneration: route.Generation,
			LastTransitionTime: metav1.Now(),
			Reason:             string(gatev1.RouteReasonUnsupportedProtocol),
			Message:            fmt.Sprintf("Cannot load GRPCBackendRef %s/%s/%s/%s port is required", group, kind, namespace, backendRef.Name),
		}
	}

	portStr := strconv.FormatInt(int64(port), 10)
	serviceName = provider.Normalize(serviceName + "-" + portStr)

	lb, err := p.loadGRPCServers(namespace, backendRef.BackendRef)
	if err != nil {
		return serviceName, nil, &metav1.Condition{
			Type:               string(gatev1.RouteConditionResolvedRefs),
			Status:             metav1.ConditionFalse,
			ObservedGeneration: route.Generation,
			LastTransitionTime: metav1.Now(),
			Reason:             string(gatev1.RouteReasonBackendNotFound),
			Message:            fmt.Sprintf("Cannot load GRPCBackendRef %s/%s/%s/%s: %s", group, kind, namespace, backendRef.Name, err),
		}
	}

	return serviceName, &dynamic.Service{LoadBalancer: lb}, nil
}

func (p *Provider) loadGRPCMiddlewares(conf *dynamic.Configuration, namespace, routerName string, filters []gatev1.GRPCRouteFilter) ([]string, error) {
	middlewares := make(map[string]*dynamic.Middleware)
	for i, filter := range filters {
		name := fmt.Sprintf("%s-%s-%d", routerName, strings.ToLower(string(filter.Type)), i)
		switch filter.Type {
		case gatev1.GRPCRouteFilterRequestHeaderModifier:
			middlewares[name] = createRequestHeaderModifier(filter.RequestHeaderModifier)

		case gatev1.GRPCRouteFilterExtensionRef:
			name, middleware, err := p.loadHTTPRouteFilterExtensionRef(namespace, filter.ExtensionRef)
			if err != nil {
				return nil, fmt.Errorf("loading ExtensionRef filter %s: %w", filter.Type, err)
			}

			middlewares[name] = middleware

		default:
			// As per the spec: https://gateway-api.sigs.k8s.io/api-types/httproute/#filters-optional
			// In all cases where incompatible or unsupported filters are
			// specified, implementations MUST add a warning condition to
			// status.
			return nil, fmt.Errorf("unsupported filter %s", filter.Type)
		}
	}

	var middlewareNames []string
	for name, middleware := range middlewares {
		if middleware != nil {
			conf.HTTP.Middlewares[name] = middleware
		}

		middlewareNames = append(middlewareNames, name)
	}

	return middlewareNames, nil
}

func (p *Provider) loadGRPCServers(namespace string, backendRef gatev1.BackendRef) (*dynamic.ServersLoadBalancer, error) {
	if backendRef.Port == nil {
		return nil, errors.New("port is required for Kubernetes Service reference")
	}

	service, exists, err := p.client.GetService(namespace, string(backendRef.Name))
	if err != nil {
		return nil, fmt.Errorf("getting service: %w", err)
	}
	if !exists {
		return nil, errors.New("service not found")
	}

	var svcPort *corev1.ServicePort
	for _, p := range service.Spec.Ports {
		if p.Port == int32(*backendRef.Port) {
			svcPort = &p
			break
		}
	}
	if svcPort == nil {
		return nil, fmt.Errorf("service port %d not found", *backendRef.Port)
	}

	endpointSlices, err := p.client.ListEndpointSlicesForService(namespace, string(backendRef.Name))
	if err != nil {
		return nil, fmt.Errorf("getting endpointslices: %w", err)
	}
	if len(endpointSlices) == 0 {
		return nil, errors.New("endpointslices not found")
	}

	lb := &dynamic.ServersLoadBalancer{}
	lb.SetDefaults()

	addresses := map[string]struct{}{}
	for _, endpointSlice := range endpointSlices {
		var port int32
		for _, p := range endpointSlice.Ports {
			if svcPort.Name == *p.Name {
				port = *p.Port
				break
			}
		}
		if port == 0 {
			continue
		}

		for _, endpoint := range endpointSlice.Endpoints {
			if endpoint.Conditions.Ready == nil || !*endpoint.Conditions.Ready {
				continue
			}

			for _, address := range endpoint.Addresses {
				if _, ok := addresses[address]; ok {
					continue
				}

				addresses[address] = struct{}{}
				lb.Servers = append(lb.Servers, dynamic.Server{
					URL: fmt.Sprintf("h2c://%s", net.JoinHostPort(address, strconv.Itoa(int(port)))),
				})
			}
		}
	}

	return lb, nil
}

// FIXME rename
// FIXME conflict with HTTPRoute if hostname intersection
func buildGRPCMatchRule(hostnames []gatev1.Hostname, match gatev1.GRPCRouteMatch) string {
	var matchRules []string

	methodRule, err := buildGRPCMethodRule(match.Method)
	if err != nil {
		// FIXME error handling
	}
	matchRules = append(matchRules, methodRule)

	headerRules := buildGRPCHeaderRules(match.Headers)
	matchRules = append(matchRules, headerRules...)

	matchRulesStr := strings.Join(matchRules, " && ")

	hostRule, _ := buildHostRule(hostnames)
	if hostRule == "" {
		return matchRulesStr
	}
	return hostRule + " && " + matchRulesStr
}

//			pathValue = "/" + *gm.Method.Service + "/" + *gm.Method.Method
//			pathType = v1.PathMatchType("Exact")

// FIXME comment on pathtype matching
func buildGRPCMethodRule(method *gatev1.GRPCMethodMatch) (string, error) {
	if method == nil {
		return "PathPrefix(`/`)", nil
	}

	typ := ptr.Deref(method.Type, gatev1.GRPCMethodMatchExact)
	if typ != gatev1.GRPCMethodMatchExact {
		return "", fmt.Errorf("unsupported GRPC method match type: %s", method.Type)
	}

	sExpr := "[^/]+"
	if s := ptr.Deref(method.Service, ""); s != "" {
		sExpr = s
	}

	mExpr := "[^/]+"
	if m := ptr.Deref(method.Method, ""); m != "" {
		mExpr = m
	}

	return fmt.Sprintf("PathRegexp(`/%s/%s`)", sExpr, mExpr), nil
}

func buildGRPCHeaderRules(headers []gatev1.GRPCHeaderMatch) []string {
	var rules []string
	for _, header := range headers {
		switch ptr.Deref(header.Type, gatev1.HeaderMatchExact) {
		case gatev1.HeaderMatchExact:
			rules = append(rules, fmt.Sprintf("Header(`%s`,`%s`)", header.Name, header.Value))
		case gatev1.HeaderMatchRegularExpression:
			rules = append(rules, fmt.Sprintf("HeaderRegexp(`%s`,`%s`)", header.Name, header.Value))
		}
	}

	return rules
}
