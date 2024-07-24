package gateway

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/traefik/traefik/v3/pkg/types"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	kerror "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	ktypes "k8s.io/apimachinery/pkg/types"
	kinformers "k8s.io/client-go/informers"
	kclientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	gatev1 "sigs.k8s.io/gateway-api/apis/v1"
	gatev1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
	gatev1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"
	gateclientset "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned"
	gateinformers "sigs.k8s.io/gateway-api/pkg/client/informers/externalversions"
)

const resyncPeriod = 10 * time.Minute

type resourceEventHandler struct {
	ev chan<- interface{}
}

func (reh *resourceEventHandler) OnAdd(obj interface{}, _ bool) {
	eventHandlerFunc(reh.ev, obj)
}

func (reh *resourceEventHandler) OnUpdate(_, newObj interface{}) {
	eventHandlerFunc(reh.ev, newObj)
}

func (reh *resourceEventHandler) OnDelete(obj interface{}) {
	eventHandlerFunc(reh.ev, obj)
}

// Client is a client for the Provider master.
// WatchAll starts the watch of the Provider resources and updates the stores.
// The stores can then be accessed via the Get* functions.
type Client interface {
	WatchAll(namespaces []string, stopCh <-chan struct{}) (<-chan interface{}, error)
	UpdateGatewayStatus(ctx context.Context, gateway ktypes.NamespacedName, status gatev1.GatewayStatus) error
	UpdateGatewayClassStatus(ctx context.Context, name string, status gatev1.GatewayClassStatus) error
	UpdateHTTPRouteStatus(ctx context.Context, route ktypes.NamespacedName, status gatev1.HTTPRouteStatus) error
	UpdateGRPCRouteStatus(ctx context.Context, route ktypes.NamespacedName, status gatev1.GRPCRouteStatus) error
	UpdateTCPRouteStatus(ctx context.Context, route ktypes.NamespacedName, status gatev1alpha2.TCPRouteStatus) error
	UpdateTLSRouteStatus(ctx context.Context, route ktypes.NamespacedName, status gatev1alpha2.TLSRouteStatus) error
	ListGatewayClasses() ([]*gatev1.GatewayClass, error)
	ListGateways() []*gatev1.Gateway
	ListHTTPRoutes() ([]*gatev1.HTTPRoute, error)
	ListGRPCRoutes() ([]*gatev1.GRPCRoute, error)
	ListTCPRoutes() ([]*gatev1alpha2.TCPRoute, error)
	ListTLSRoutes() ([]*gatev1alpha2.TLSRoute, error)
	ListNamespaces(selector labels.Selector) ([]string, error)
	ListReferenceGrants(namespace string) ([]*gatev1beta1.ReferenceGrant, error)
	ListEndpointSlicesForService(namespace, serviceName string) ([]*discoveryv1.EndpointSlice, error)
	GetService(namespace, name string) (*corev1.Service, bool, error)
	GetSecret(namespace, name string) (*corev1.Secret, bool, error)
}

type clientWrapper struct {
	csGateway gateclientset.Interface
	csKube    kclientset.Interface

	factoryNamespace    kinformers.SharedInformerFactory
	factoryGatewayClass gateinformers.SharedInformerFactory
	factoriesGateway    map[string]gateinformers.SharedInformerFactory
	factoriesKube       map[string]kinformers.SharedInformerFactory
	factoriesSecret     map[string]kinformers.SharedInformerFactory

	isNamespaceAll    bool
	watchedNamespaces []string

	labelSelector       string
	experimentalChannel bool
}

func createClientFromConfig(c *rest.Config) (*clientWrapper, error) {
	csGateway, err := gateclientset.NewForConfig(c)
	if err != nil {
		return nil, err
	}

	csKube, err := kclientset.NewForConfig(c)
	if err != nil {
		return nil, err
	}

	return newClientImpl(csKube, csGateway), nil
}

func newClientImpl(csKube kclientset.Interface, csGateway gateclientset.Interface) *clientWrapper {
	return &clientWrapper{
		csGateway:        csGateway,
		csKube:           csKube,
		factoriesGateway: make(map[string]gateinformers.SharedInformerFactory),
		factoriesKube:    make(map[string]kinformers.SharedInformerFactory),
		factoriesSecret:  make(map[string]kinformers.SharedInformerFactory),
	}
}

// newInClusterClient returns a new Provider client that is expected to run
// inside the cluster.
func newInClusterClient(endpoint string) (*clientWrapper, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to create in-cluster configuration: %w", err)
	}

	if endpoint != "" {
		config.Host = endpoint
	}

	return createClientFromConfig(config)
}

func newExternalClusterClientFromFile(file string) (*clientWrapper, error) {
	configFromFlags, err := clientcmd.BuildConfigFromFlags("", file)
	if err != nil {
		return nil, err
	}
	return createClientFromConfig(configFromFlags)
}

// newExternalClusterClient returns a new Provider client that may run outside of the cluster.
// The endpoint parameter must not be empty.
func newExternalClusterClient(endpoint, caFilePath string, token types.FileOrContent) (*clientWrapper, error) {
	if endpoint == "" {
		return nil, errors.New("endpoint missing for external cluster client")
	}

	tokenData, err := token.Read()
	if err != nil {
		return nil, fmt.Errorf("read token: %w", err)
	}

	config := &rest.Config{
		Host:        endpoint,
		BearerToken: string(tokenData),
	}

	if caFilePath != "" {
		caData, err := os.ReadFile(caFilePath)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA file %s: %w", caFilePath, err)
		}

		config.TLSClientConfig = rest.TLSClientConfig{CAData: caData}
	}

	return createClientFromConfig(config)
}

// WatchAll starts namespace-specific controllers for all relevant kinds.
func (c *clientWrapper) WatchAll(namespaces []string, stopCh <-chan struct{}) (<-chan interface{}, error) {
	eventCh := make(chan interface{}, 1)
	eventHandler := &resourceEventHandler{ev: eventCh}

	if len(namespaces) == 0 {
		namespaces = []string{metav1.NamespaceAll}
		c.isNamespaceAll = true
	}

	c.watchedNamespaces = namespaces

	notOwnedByHelm := func(opts *metav1.ListOptions) {
		opts.LabelSelector = "owner!=helm"
	}

	labelSelectorOptions := func(options *metav1.ListOptions) {
		options.LabelSelector = c.labelSelector
	}

	c.factoryNamespace = kinformers.NewSharedInformerFactory(c.csKube, resyncPeriod)
	_, err := c.factoryNamespace.Core().V1().Namespaces().Informer().AddEventHandler(eventHandler)
	if err != nil {
		return nil, err
	}

	c.factoryGatewayClass = gateinformers.NewSharedInformerFactoryWithOptions(c.csGateway, resyncPeriod, gateinformers.WithTweakListOptions(labelSelectorOptions))
	_, err = c.factoryGatewayClass.Gateway().V1().GatewayClasses().Informer().AddEventHandler(eventHandler)
	if err != nil {
		return nil, err
	}

	for _, ns := range namespaces {
		factoryGateway := gateinformers.NewSharedInformerFactoryWithOptions(c.csGateway, resyncPeriod, gateinformers.WithNamespace(ns))
		_, err = factoryGateway.Gateway().V1().Gateways().Informer().AddEventHandler(eventHandler)
		if err != nil {
			return nil, err
		}
		_, err = factoryGateway.Gateway().V1().HTTPRoutes().Informer().AddEventHandler(eventHandler)
		if err != nil {
			return nil, err
		}
		_, err = factoryGateway.Gateway().V1().GRPCRoutes().Informer().AddEventHandler(eventHandler)
		if err != nil {
			return nil, err
		}
		_, err = factoryGateway.Gateway().V1beta1().ReferenceGrants().Informer().AddEventHandler(eventHandler)
		if err != nil {
			return nil, err
		}

		if c.experimentalChannel {
			_, err = factoryGateway.Gateway().V1alpha2().TCPRoutes().Informer().AddEventHandler(eventHandler)
			if err != nil {
				return nil, err
			}
			_, err = factoryGateway.Gateway().V1alpha2().TLSRoutes().Informer().AddEventHandler(eventHandler)
			if err != nil {
				return nil, err
			}
		}

		factoryKube := kinformers.NewSharedInformerFactoryWithOptions(c.csKube, resyncPeriod, kinformers.WithNamespace(ns))
		_, err = factoryKube.Core().V1().Services().Informer().AddEventHandler(eventHandler)
		if err != nil {
			return nil, err
		}
		_, err = factoryKube.Discovery().V1().EndpointSlices().Informer().AddEventHandler(eventHandler)
		if err != nil {
			return nil, err
		}

		factorySecret := kinformers.NewSharedInformerFactoryWithOptions(c.csKube, resyncPeriod, kinformers.WithNamespace(ns), kinformers.WithTweakListOptions(notOwnedByHelm))
		_, err = factorySecret.Core().V1().Secrets().Informer().AddEventHandler(eventHandler)
		if err != nil {
			return nil, err
		}

		c.factoriesGateway[ns] = factoryGateway
		c.factoriesKube[ns] = factoryKube
		c.factoriesSecret[ns] = factorySecret
	}

	c.factoryNamespace.Start(stopCh)
	c.factoryGatewayClass.Start(stopCh)

	for _, ns := range namespaces {
		c.factoriesGateway[ns].Start(stopCh)
		c.factoriesKube[ns].Start(stopCh)
		c.factoriesSecret[ns].Start(stopCh)
	}

	for t, ok := range c.factoryNamespace.WaitForCacheSync(stopCh) {
		if !ok {
			return nil, fmt.Errorf("timed out waiting for controller caches to sync %s", t.String())
		}
	}

	for t, ok := range c.factoryGatewayClass.WaitForCacheSync(stopCh) {
		if !ok {
			return nil, fmt.Errorf("timed out waiting for controller caches to sync %s", t.String())
		}
	}

	for _, ns := range namespaces {
		for t, ok := range c.factoriesGateway[ns].WaitForCacheSync(stopCh) {
			if !ok {
				return nil, fmt.Errorf("timed out waiting for controller caches to sync %s in namespace %q", t.String(), ns)
			}
		}

		for t, ok := range c.factoriesKube[ns].WaitForCacheSync(stopCh) {
			if !ok {
				return nil, fmt.Errorf("timed out waiting for controller caches to sync %s in namespace %q", t.String(), ns)
			}
		}

		for t, ok := range c.factoriesSecret[ns].WaitForCacheSync(stopCh) {
			if !ok {
				return nil, fmt.Errorf("timed out waiting for controller caches to sync %s in namespace %q", t.String(), ns)
			}
		}
	}

	return eventCh, nil
}

func (c *clientWrapper) ListNamespaces(selector labels.Selector) ([]string, error) {
	ns, err := c.factoryNamespace.Core().V1().Namespaces().Lister().List(selector)
	if err != nil {
		return nil, err
	}

	var namespaces []string
	for _, namespace := range ns {
		if !c.isWatchedNamespace(namespace.Name) {
			log.Warn().Msgf("Namespace %q is not within %q watched namespaces", selector, namespace)
			continue
		}
		namespaces = append(namespaces, namespace.Name)
	}
	return namespaces, nil
}

func (c *clientWrapper) ListHTTPRoutes() ([]*gatev1.HTTPRoute, error) {
	var httpRoutes []*gatev1.HTTPRoute
	for _, namespace := range c.watchedNamespaces {
		routes, err := c.factoriesGateway[c.lookupNamespace(namespace)].Gateway().V1().HTTPRoutes().Lister().HTTPRoutes(namespace).List(labels.Everything())
		if err != nil {
			return nil, fmt.Errorf("listing HTTP routes in namespace %s", namespace)
		}

		httpRoutes = append(httpRoutes, routes...)
	}

	return httpRoutes, nil
}

func (c *clientWrapper) ListGRPCRoutes() ([]*gatev1.GRPCRoute, error) {
	var grpcRoutes []*gatev1.GRPCRoute
	for _, namespace := range c.watchedNamespaces {
		routes, err := c.factoriesGateway[c.lookupNamespace(namespace)].Gateway().V1().GRPCRoutes().Lister().GRPCRoutes(namespace).List(labels.Everything())
		if err != nil {
			return nil, fmt.Errorf("listing GRPC routes in namespace %s", namespace)
		}

		grpcRoutes = append(grpcRoutes, routes...)
	}

	return grpcRoutes, nil
}

func (c *clientWrapper) ListTCPRoutes() ([]*gatev1alpha2.TCPRoute, error) {
	var tcpRoutes []*gatev1alpha2.TCPRoute
	for _, namespace := range c.watchedNamespaces {
		routes, err := c.factoriesGateway[c.lookupNamespace(namespace)].Gateway().V1alpha2().TCPRoutes().Lister().TCPRoutes(namespace).List(labels.Everything())
		if err != nil {
			return nil, fmt.Errorf("listing TCP routes in namespace %s", namespace)
		}

		tcpRoutes = append(tcpRoutes, routes...)
	}

	return tcpRoutes, nil
}

func (c *clientWrapper) ListTLSRoutes() ([]*gatev1alpha2.TLSRoute, error) {
	var tlsRoutes []*gatev1alpha2.TLSRoute
	for _, namespace := range c.watchedNamespaces {
		routes, err := c.factoriesGateway[c.lookupNamespace(namespace)].Gateway().V1alpha2().TLSRoutes().Lister().TLSRoutes(namespace).List(labels.Everything())
		if err != nil {
			return nil, fmt.Errorf("listing TLS routes in namespace %s", namespace)
		}

		tlsRoutes = append(tlsRoutes, routes...)
	}

	return tlsRoutes, nil
}

func (c *clientWrapper) ListReferenceGrants(namespace string) ([]*gatev1beta1.ReferenceGrant, error) {
	if !c.isWatchedNamespace(namespace) {
		log.Warn().Msgf("Failed to get ReferenceGrants: %q is not within watched namespaces", namespace)

		return nil, fmt.Errorf("failed to get ReferenceGrants: namespace %s is not within watched namespaces", namespace)
	}

	referenceGrants, err := c.factoriesGateway[c.lookupNamespace(namespace)].Gateway().V1beta1().ReferenceGrants().Lister().ReferenceGrants(namespace).List(labels.Everything())
	if err != nil {
		return nil, err
	}

	return referenceGrants, nil
}

func (c *clientWrapper) ListGateways() []*gatev1.Gateway {
	var result []*gatev1.Gateway

	for ns, factory := range c.factoriesGateway {
		gateways, err := factory.Gateway().V1().Gateways().Lister().Gateways(ns).List(labels.Everything())
		if err != nil {
			log.Error().Err(err).Msgf("Failed to list Gateways in namespace %s", ns)
			continue
		}
		result = append(result, gateways...)
	}

	return result
}

func (c *clientWrapper) ListGatewayClasses() ([]*gatev1.GatewayClass, error) {
	return c.factoryGatewayClass.Gateway().V1().GatewayClasses().Lister().List(labels.Everything())
}

func (c *clientWrapper) UpdateGatewayClassStatus(ctx context.Context, name string, status gatev1.GatewayClassStatus) error {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentGatewayClass, err := c.factoryGatewayClass.Gateway().V1().GatewayClasses().Lister().Get(name)
		if err != nil {
			// We have to return err itself here (not wrapped inside another error)
			// so that RetryOnConflict can identify it correctly.
			return err
		}

		if conditionsEqual(currentGatewayClass.Status.Conditions, status.Conditions) {
			return nil
		}

		currentGatewayClass = currentGatewayClass.DeepCopy()
		currentGatewayClass.Status = status

		if _, err = c.csGateway.GatewayV1().GatewayClasses().UpdateStatus(ctx, currentGatewayClass, metav1.UpdateOptions{}); err != nil {
			// We have to return err itself here (not wrapped inside another error)
			// so that RetryOnConflict can identify it correctly.
			return err
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to update GatewayClass %q status: %w", name, err)
	}

	return nil
}

func (c *clientWrapper) UpdateGatewayStatus(ctx context.Context, gateway ktypes.NamespacedName, status gatev1.GatewayStatus) error {
	if !c.isWatchedNamespace(gateway.Namespace) {
		return fmt.Errorf("cannot update Gateway status %s/%s: namespace is not within watched namespaces", gateway.Namespace, gateway.Name)
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentGateway, err := c.factoriesGateway[c.lookupNamespace(gateway.Namespace)].Gateway().V1().Gateways().Lister().Gateways(gateway.Namespace).Get(gateway.Name)
		if err != nil {
			// We have to return err itself here (not wrapped inside another error)
			// so that RetryOnConflict can identify it correctly.
			return err
		}

		if gatewayStatusEqual(currentGateway.Status, status) {
			return nil
		}

		currentGateway = currentGateway.DeepCopy()
		currentGateway.Status = status

		if _, err = c.csGateway.GatewayV1().Gateways(gateway.Namespace).UpdateStatus(ctx, currentGateway, metav1.UpdateOptions{}); err != nil {
			// We have to return err itself here (not wrapped inside another error)
			// so that RetryOnConflict can identify it correctly.
			return err
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to update Gateway %q status: %w", gateway.Name, err)
	}

	return nil
}

func (c *clientWrapper) UpdateHTTPRouteStatus(ctx context.Context, route ktypes.NamespacedName, status gatev1.HTTPRouteStatus) error {
	if !c.isWatchedNamespace(route.Namespace) {
		return fmt.Errorf("updating HTTPRoute status %s/%s: namespace is not within watched namespaces", route.Namespace, route.Name)
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentRoute, err := c.factoriesGateway[c.lookupNamespace(route.Namespace)].Gateway().V1().HTTPRoutes().Lister().HTTPRoutes(route.Namespace).Get(route.Name)
		if err != nil {
			// We have to return err itself here (not wrapped inside another error)
			// so that RetryOnConflict can identify it correctly.
			return err
		}

		parentStatuses := make([]gatev1.RouteParentStatus, len(status.Parents))
		copy(parentStatuses, status.Parents)

		// keep statuses added by other gateway controllers.
		// TODO: we should also keep statuses for gateways managed by other Traefik instances.
		for _, parentStatus := range currentRoute.Status.Parents {
			if parentStatus.ControllerName != controllerName {
				parentStatuses = append(parentStatuses, parentStatus)
				continue
			}
		}

		// do not update status when nothing has changed.
		if routeParentStatusesEqual(currentRoute.Status.Parents, parentStatuses) {
			return nil
		}

		currentRoute = currentRoute.DeepCopy()
		currentRoute.Status = gatev1.HTTPRouteStatus{
			RouteStatus: gatev1.RouteStatus{
				Parents: parentStatuses,
			},
		}

		if _, err = c.csGateway.GatewayV1().HTTPRoutes(route.Namespace).UpdateStatus(ctx, currentRoute, metav1.UpdateOptions{}); err != nil {
			// We have to return err itself here (not wrapped inside another error)
			// so that RetryOnConflict can identify it correctly.
			return err
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to update HTTPRoute %q status: %w", route.Name, err)
	}

	return nil
}

func (c *clientWrapper) UpdateGRPCRouteStatus(ctx context.Context, route ktypes.NamespacedName, status gatev1.GRPCRouteStatus) error {
	if !c.isWatchedNamespace(route.Namespace) {
		return fmt.Errorf("updating GRPCRoute status %s/%s: namespace is not within watched namespaces", route.Namespace, route.Name)
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentRoute, err := c.factoriesGateway[c.lookupNamespace(route.Namespace)].Gateway().V1().GRPCRoutes().Lister().GRPCRoutes(route.Namespace).Get(route.Name)
		if err != nil {
			// We have to return err itself here (not wrapped inside another error)
			// so that RetryOnConflict can identify it correctly.
			return err
		}

		// TODO: keep statuses for gateways managed by other Traefik instances.
		var parentStatuses []gatev1.RouteParentStatus
		for _, currentParentStatus := range currentRoute.Status.Parents {
			if currentParentStatus.ControllerName != controllerName {
				parentStatuses = append(parentStatuses, currentParentStatus)
				continue
			}
		}

		parentStatuses = append(parentStatuses, status.Parents...)

		currentRoute = currentRoute.DeepCopy()
		currentRoute.Status = gatev1.GRPCRouteStatus{
			RouteStatus: gatev1.RouteStatus{
				Parents: parentStatuses,
			},
		}

		if _, err = c.csGateway.GatewayV1().GRPCRoutes(route.Namespace).UpdateStatus(ctx, currentRoute, metav1.UpdateOptions{}); err != nil {
			// We have to return err itself here (not wrapped inside another error)
			// so that RetryOnConflict can identify it correctly.
			return err
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to update GRPCRoute %q status: %w", route.Name, err)
	}

	return nil
}

func (c *clientWrapper) UpdateTCPRouteStatus(ctx context.Context, route ktypes.NamespacedName, status gatev1alpha2.TCPRouteStatus) error {
	if !c.isWatchedNamespace(route.Namespace) {
		return fmt.Errorf("updating TCPRoute status %s/%s: namespace is not within watched namespaces", route.Namespace, route.Name)
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentRoute, err := c.factoriesGateway[c.lookupNamespace(route.Namespace)].Gateway().V1alpha2().TCPRoutes().Lister().TCPRoutes(route.Namespace).Get(route.Name)
		if err != nil {
			// We have to return err itself here (not wrapped inside another error)
			// so that RetryOnConflict can identify it correctly.
			return err
		}

		parentStatuses := make([]gatev1.RouteParentStatus, len(status.Parents))
		copy(parentStatuses, status.Parents)

		// keep statuses added by other gateway controllers.
		// TODO: we should also keep statuses for gateways managed by other Traefik instances.
		for _, parentStatus := range currentRoute.Status.Parents {
			if parentStatus.ControllerName != controllerName {
				parentStatuses = append(parentStatuses, parentStatus)
				continue
			}
		}

		// do not update status when nothing has changed.
		if routeParentStatusesEqual(currentRoute.Status.Parents, parentStatuses) {
			return nil
		}

		currentRoute = currentRoute.DeepCopy()
		currentRoute.Status = gatev1alpha2.TCPRouteStatus{
			RouteStatus: gatev1.RouteStatus{
				Parents: parentStatuses,
			},
		}

		if _, err = c.csGateway.GatewayV1alpha2().TCPRoutes(route.Namespace).UpdateStatus(ctx, currentRoute, metav1.UpdateOptions{}); err != nil {
			// We have to return err itself here (not wrapped inside another error)
			// so that RetryOnConflict can identify it correctly.
			return err
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to update TCPRoute %q status: %w", route.Name, err)
	}

	return nil
}

func (c *clientWrapper) UpdateTLSRouteStatus(ctx context.Context, route ktypes.NamespacedName, status gatev1alpha2.TLSRouteStatus) error {
	if !c.isWatchedNamespace(route.Namespace) {
		return fmt.Errorf("updating TLSRoute status %s/%s: namespace is not within watched namespaces", route.Namespace, route.Name)
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentRoute, err := c.factoriesGateway[c.lookupNamespace(route.Namespace)].Gateway().V1alpha2().TLSRoutes().Lister().TLSRoutes(route.Namespace).Get(route.Name)
		if err != nil {
			// We have to return err itself here (not wrapped inside another error)
			// so that RetryOnConflict can identify it correctly.
			return err
		}

		parentStatuses := make([]gatev1.RouteParentStatus, len(status.Parents))
		copy(parentStatuses, status.Parents)

		// keep statuses added by other gateway controllers.
		// TODO: we should also keep statuses for gateways managed by other Traefik instances.
		for _, parentStatus := range currentRoute.Status.Parents {
			if parentStatus.ControllerName != controllerName {
				parentStatuses = append(parentStatuses, parentStatus)
				continue
			}
		}

		// do not update status when nothing has changed.
		if routeParentStatusesEqual(currentRoute.Status.Parents, parentStatuses) {
			return nil
		}

		currentRoute = currentRoute.DeepCopy()
		currentRoute.Status = gatev1alpha2.TLSRouteStatus{
			RouteStatus: gatev1.RouteStatus{
				Parents: parentStatuses,
			},
		}

		if _, err = c.csGateway.GatewayV1alpha2().TLSRoutes(route.Namespace).UpdateStatus(ctx, currentRoute, metav1.UpdateOptions{}); err != nil {
			// We have to return err itself here (not wrapped inside another error)
			// so that RetryOnConflict can identify it correctly.
			return err
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to update TLSRoute %q status: %w", route.Name, err)
	}

	return nil
}

// GetService returns the named service from the given namespace.
func (c *clientWrapper) GetService(namespace, name string) (*corev1.Service, bool, error) {
	if !c.isWatchedNamespace(namespace) {
		return nil, false, fmt.Errorf("failed to get service %s/%s: namespace is not within watched namespaces", namespace, name)
	}

	service, err := c.factoriesKube[c.lookupNamespace(namespace)].Core().V1().Services().Lister().Services(namespace).Get(name)
	exist, err := translateNotFoundError(err)

	return service, exist, err
}

// ListEndpointSlicesForService returns the EndpointSlices for the given service name in the given namespace.
func (c *clientWrapper) ListEndpointSlicesForService(namespace, serviceName string) ([]*discoveryv1.EndpointSlice, error) {
	if !c.isWatchedNamespace(namespace) {
		return nil, fmt.Errorf("failed to get endpointslices for service %s/%s: namespace is not within watched namespaces", namespace, serviceName)
	}

	serviceLabelRequirement, err := labels.NewRequirement(discoveryv1.LabelServiceName, selection.Equals, []string{serviceName})
	if err != nil {
		return nil, fmt.Errorf("failed to create service label selector requirement: %w", err)
	}
	serviceSelector := labels.NewSelector()
	serviceSelector = serviceSelector.Add(*serviceLabelRequirement)

	return c.factoriesKube[c.lookupNamespace(namespace)].Discovery().V1().EndpointSlices().Lister().EndpointSlices(namespace).List(serviceSelector)
}

// GetSecret returns the named secret from the given namespace.
func (c *clientWrapper) GetSecret(namespace, name string) (*corev1.Secret, bool, error) {
	if !c.isWatchedNamespace(namespace) {
		return nil, false, fmt.Errorf("failed to get secret %s/%s: namespace is not within watched namespaces", namespace, name)
	}

	secret, err := c.factoriesSecret[c.lookupNamespace(namespace)].Core().V1().Secrets().Lister().Secrets(namespace).Get(name)
	exist, err := translateNotFoundError(err)

	return secret, exist, err
}

// lookupNamespace returns the lookup namespace key for the given namespace.
// When listening on all namespaces, it returns the client-go identifier ("")
// for all-namespaces. Otherwise, it returns the given namespace.
// The distinction is necessary because we index all informers on the special
// identifier iff all-namespaces are requested but receive specific namespace
// identifiers from the Kubernetes API, so we have to bridge this gap.
func (c *clientWrapper) lookupNamespace(namespace string) string {
	if c.isNamespaceAll {
		return metav1.NamespaceAll
	}
	return namespace
}

// isWatchedNamespace checks to ensure that the namespace is being watched before we request
// it to ensure we don't panic by requesting an out-of-watch object.
func (c *clientWrapper) isWatchedNamespace(namespace string) bool {
	if c.isNamespaceAll {
		return true
	}

	return slices.Contains(c.watchedNamespaces, namespace)
}

// eventHandlerFunc will pass the obj on to the events channel or drop it.
// This is so passing the events along won't block in the case of high volume.
// The events are only used for signaling anyway so dropping a few is ok.
func eventHandlerFunc(events chan<- interface{}, obj interface{}) {
	select {
	case events <- obj:
	default:
	}
}

// translateNotFoundError will translate a "not found" error to a boolean return
// value which indicates if the resource exists and a nil error.
func translateNotFoundError(err error) (bool, error) {
	if kerror.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

func gatewayStatusEqual(statusA, statusB gatev1.GatewayStatus) bool {
	if len(statusA.Listeners) != len(statusB.Listeners) {
		return false
	}

	if !conditionsEqual(statusA.Conditions, statusB.Conditions) {
		return false
	}

	listenerMatches := 0
	for _, newListener := range statusB.Listeners {
		for _, oldListener := range statusA.Listeners {
			if newListener.Name == oldListener.Name {
				if !conditionsEqual(newListener.Conditions, oldListener.Conditions) {
					return false
				}

				if newListener.AttachedRoutes != oldListener.AttachedRoutes {
					return false
				}

				listenerMatches++
			}
		}
	}

	return listenerMatches == len(statusA.Listeners)
}

func routeParentStatusesEqual(routeParentStatusesA, routeParentStatusesB []gatev1alpha2.RouteParentStatus) bool {
	if len(routeParentStatusesA) != len(routeParentStatusesB) {
		return false
	}

	for _, sA := range routeParentStatusesA {
		if !slices.ContainsFunc(routeParentStatusesB, func(sB gatev1alpha2.RouteParentStatus) bool {
			return routeParentStatusEqual(sB, sA)
		}) {
			return false
		}
	}

	for _, sB := range routeParentStatusesB {
		if !slices.ContainsFunc(routeParentStatusesA, func(sA gatev1alpha2.RouteParentStatus) bool {
			return routeParentStatusEqual(sA, sB)
		}) {
			return false
		}
	}

	return true
}

func routeParentStatusEqual(sA, sB gatev1alpha2.RouteParentStatus) bool {
	if !reflect.DeepEqual(sA.ParentRef, sB.ParentRef) {
		return false
	}

	if sA.ControllerName != sB.ControllerName {
		return false
	}

	return conditionsEqual(sA.Conditions, sB.Conditions)
}

func conditionsEqual(conditionsA, conditionsB []metav1.Condition) bool {
	return slices.EqualFunc(conditionsA, conditionsB, func(cA metav1.Condition, cB metav1.Condition) bool {
		return cA.Type == cB.Type &&
			cA.Reason == cB.Reason &&
			cA.Status == cB.Status &&
			cA.Message == cB.Message &&
			cA.ObservedGeneration == cB.ObservedGeneration
	})
}
