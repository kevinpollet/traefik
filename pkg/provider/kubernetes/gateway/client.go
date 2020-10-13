package gateway

import (
	"errors"
	"fmt"
	"io/ioutil"
	"time"

	"github.com/traefik/traefik/v2/pkg/log"
	corev1 "k8s.io/api/core/v1"
	kubeerror "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/service-apis/apis/v1alpha1"
	"sigs.k8s.io/service-apis/pkg/client/clientset/versioned"
	"sigs.k8s.io/service-apis/pkg/client/informers/externalversions"
)

const resyncPeriod = 10 * time.Minute

type resourceEventHandler struct {
	ev chan<- interface{}
}

func (reh *resourceEventHandler) OnAdd(obj interface{}) {
	eventHandlerFunc(reh.ev, obj)
}

func (reh *resourceEventHandler) OnUpdate(oldObj, newObj interface{}) {
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

	GetGatewayClasses() []*v1alpha1.GatewayClass
	GetGateways() []*v1alpha1.Gateway
	GetHTTPRoutes(namespace string, selector labels.Selector) ([]*v1alpha1.HTTPRoute, bool, error)

	GetService(namespace, name string) (*corev1.Service, bool, error)
	GetSecret(namespace, name string) (*corev1.Secret, bool, error)
	GetEndpoints(namespace, name string) (*corev1.Endpoints, bool, error)
}

type clientWrapper struct {
	csGateway *versioned.Clientset
	csKube    *kubernetes.Clientset

	factoriesGateway map[string]externalversions.SharedInformerFactory
	factoriesKube    map[string]informers.SharedInformerFactory

	labelSelector labels.Selector

	isNamespaceAll    bool
	watchedNamespaces []string
}

func createClientFromConfig(c *rest.Config) (*clientWrapper, error) {
	csCrd, err := versioned.NewForConfig(c)
	if err != nil {
		return nil, err
	}

	csKube, err := kubernetes.NewForConfig(c)
	if err != nil {
		return nil, err
	}

	return newClientImpl(csKube, csCrd), nil
}

func newClientImpl(csKube *kubernetes.Clientset, csCrd *versioned.Clientset) *clientWrapper {
	return &clientWrapper{
		csGateway:        csCrd,
		csKube:           csKube,
		factoriesGateway: make(map[string]externalversions.SharedInformerFactory),
		factoriesKube:    make(map[string]informers.SharedInformerFactory),
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

// newExternalClusterClient returns a new Provider client that may run outside
// of the cluster.
// The endpoint parameter must not be empty.
func newExternalClusterClient(endpoint, token, caFilePath string) (*clientWrapper, error) {
	if endpoint == "" {
		return nil, errors.New("endpoint missing for external cluster client")
	}

	config := &rest.Config{
		Host:        endpoint,
		BearerToken: token,
	}

	if caFilePath != "" {
		caData, err := ioutil.ReadFile(caFilePath)
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
	eventHandler := c.newResourceEventHandler(eventCh)

	if len(namespaces) == 0 {
		namespaces = []string{metav1.NamespaceAll}
		c.isNamespaceAll = true
	}
	c.watchedNamespaces = namespaces

	for _, ns := range namespaces {
		factoryCrd := externalversions.NewSharedInformerFactoryWithOptions(c.csGateway, resyncPeriod, externalversions.WithNamespace(ns))
		factoryCrd.Networking().V1alpha1().GatewayClasses().Informer().AddEventHandler(eventHandler)
		factoryCrd.Networking().V1alpha1().Gateways().Informer().AddEventHandler(eventHandler)
		factoryCrd.Networking().V1alpha1().HTTPRoutes().Informer().AddEventHandler(eventHandler)

		factoryKube := informers.NewSharedInformerFactoryWithOptions(c.csKube, resyncPeriod, informers.WithNamespace(ns))
		factoryKube.Core().V1().Services().Informer().AddEventHandler(eventHandler)
		factoryKube.Core().V1().Endpoints().Informer().AddEventHandler(eventHandler)
		factoryKube.Core().V1().Secrets().Informer().AddEventHandler(eventHandler)

		c.factoriesGateway[ns] = factoryCrd
		c.factoriesKube[ns] = factoryKube
	}

	for _, ns := range namespaces {
		c.factoriesGateway[ns].Start(stopCh)
		c.factoriesKube[ns].Start(stopCh)
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
	}

	return eventCh, nil
}

func (c *clientWrapper) GetHTTPRoutes(namespace string, selector labels.Selector) ([]*v1alpha1.HTTPRoute, bool, error) {
	if !c.isWatchedNamespace(namespace) {
		return nil, false, fmt.Errorf("failed to get HTTPRoute %s with labels selector %s: namespace is not within watched namespaces", namespace, selector)
	}

	service, err := c.factoriesGateway[c.lookupNamespace(namespace)].Networking().V1alpha1().HTTPRoutes().Lister().HTTPRoutes(namespace).List(selector)
	exist, err := translateNotFoundError(err)

	return service, exist, err
}

func (c *clientWrapper) GetGateways() []*v1alpha1.Gateway {
	var result []*v1alpha1.Gateway

	for ns, factory := range c.factoriesGateway {
		gateways, err := factory.Networking().V1alpha1().Gateways().Lister().List(c.labelSelector)
		if err != nil {
			log.WithoutContext().Errorf("Failed to list Gateways in namespace %s: %v", ns, err)
		}
		result = append(result, gateways...)
	}

	return result
}

func (c *clientWrapper) GetGatewayClasses() []*v1alpha1.GatewayClass {
	var result []*v1alpha1.GatewayClass

	for ns, factory := range c.factoriesGateway {
		gatewayClasses, err := factory.Networking().V1alpha1().GatewayClasses().Lister().List(c.labelSelector)
		if err != nil {
			log.WithoutContext().Errorf("Failed to list GatewayClasses in namespace %s: %v", ns, err)
		}
		result = append(result, gatewayClasses...)
	}

	return result
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

// GetEndpoints returns the named endpoints from the given namespace.
func (c *clientWrapper) GetEndpoints(namespace, name string) (*corev1.Endpoints, bool, error) {
	if !c.isWatchedNamespace(namespace) {
		return nil, false, fmt.Errorf("failed to get endpoints %s/%s: namespace is not within watched namespaces", namespace, name)
	}

	endpoint, err := c.factoriesKube[c.lookupNamespace(namespace)].Core().V1().Endpoints().Lister().Endpoints(namespace).Get(name)
	exist, err := translateNotFoundError(err)
	return endpoint, exist, err
}

// GetSecret returns the named secret from the given namespace.
func (c *clientWrapper) GetSecret(namespace, name string) (*corev1.Secret, bool, error) {
	if !c.isWatchedNamespace(namespace) {
		return nil, false, fmt.Errorf("failed to get secret %s/%s: namespace is not within watched namespaces", namespace, name)
	}

	secret, err := c.factoriesKube[c.lookupNamespace(namespace)].Core().V1().Secrets().Lister().Secrets(namespace).Get(name)
	exist, err := translateNotFoundError(err)
	return secret, exist, err
}

// lookupNamespace returns the lookup namespace key for the given namespace.
// When listening on all namespaces, it returns the client-go identifier ("")
// for all-namespaces. Otherwise, it returns the given namespace.
// The distinction is necessary because we index all informers on the special
// identifier iff all-namespaces are requested but receive specific namespace
// identifiers from the Kubernetes API, so we have to bridge this gap.
func (c *clientWrapper) lookupNamespace(ns string) string {
	if c.isNamespaceAll {
		return metav1.NamespaceAll
	}
	return ns
}

func (c *clientWrapper) newResourceEventHandler(events chan<- interface{}) cache.ResourceEventHandler {
	return &cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			// Ignore Ingresses that do not match our custom label selector.
			switch v := obj.(type) {
			case *v1alpha1.GatewayClass:
				return c.labelSelector.Matches(labels.Set(v.GetLabels()))
			case *v1alpha1.Gateway:
				return c.labelSelector.Matches(labels.Set(v.GetLabels()))
			case *v1alpha1.HTTPRoute:
				return c.labelSelector.Matches(labels.Set(v.GetLabels()))
			default:
				return true
			}
		},
		Handler: &resourceEventHandler{ev: events},
	}
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
	if kubeerror.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// isWatchedNamespace checks to ensure that the namespace is being watched before we request
// it to ensure we don't panic by requesting an out-of-watch object.
func (c *clientWrapper) isWatchedNamespace(ns string) bool {
	if c.isNamespaceAll {
		return true
	}
	for _, watchedNamespace := range c.watchedNamespaces {
		if watchedNamespace == ns {
			return true
		}
	}
	return false
}
