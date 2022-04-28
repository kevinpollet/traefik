package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/traefik/traefik/v2/pkg/config/dynamic"
	"github.com/traefik/traefik/v2/pkg/log"
	"github.com/traefik/traefik/v2/pkg/safe"
	"github.com/traefik/traefik/v2/pkg/tls"
)

// MultiProvider represents multi-provider instance.
type MultiProvider struct {
	Provider
}

// Provide calls the provider Provide method and intercepts its configuration message to sanitize it.
func (m MultiProvider) Provide(configurationChan chan<- dynamic.Message, pool *safe.Pool) error {
	localChan := make(chan dynamic.Message, 1)
	pool.GoCtx(func(ctx context.Context) {
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-localChan:
				msg.Configuration = sanitizeReferences(msg.ProviderName, msg.Configuration)
				configurationChan <- msg
			}
		}
	})

	return m.Provider.Provide(configurationChan, pool)
}

// sanitizeReferences removes disallowed cross provider references.
// TODO handle copy of models ?
func sanitizeReferences(pvd string, configuration *dynamic.Configuration) *dynamic.Configuration {
	conf := dynamic.Configuration{
		HTTP: &dynamic.HTTPConfiguration{
			Routers:           make(map[string]*dynamic.Router),
			Middlewares:       make(map[string]*dynamic.Middleware),
			Services:          make(map[string]*dynamic.Service),
			Models:            make(map[string]*dynamic.Model),
			ServersTransports: make(map[string]*dynamic.ServersTransport),
		},
		TCP: &dynamic.TCPConfiguration{
			Routers:     make(map[string]*dynamic.TCPRouter),
			Services:    make(map[string]*dynamic.TCPService),
			Middlewares: make(map[string]*dynamic.TCPMiddleware),
		},
		UDP: &dynamic.UDPConfiguration{
			Routers:  make(map[string]*dynamic.UDPRouter),
			Services: make(map[string]*dynamic.UDPService),
		},
		TLS: &dynamic.TLSConfiguration{
			Stores:  make(map[string]tls.Store),
			Options: make(map[string]tls.Options),
		},
	}

	ctx := log.With(context.Background(), log.Str(log.ProviderName, pvd))

	if configuration.HTTP != nil {
		excludedMiddlewares := make(map[string]struct{})
		for middlewareName, middleware := range configuration.HTTP.Middlewares {
			if err := checkHTTPMiddleware(pvd, middlewareName, conf.HTTP.Middlewares); err != nil {
				excludedMiddlewares[middlewareName] = struct{}{}
				log.FromContext(ctx).Errorf("Invalid middleware %q configuration: %s", middlewareName, err)
				continue
			}

			conf.HTTP.Middlewares[middlewareName] = middleware
		}

		excludedServices := make(map[string]struct{})
		for serviceName, service := range configuration.HTTP.Services {
			if err := checkHTTPService(pvd, serviceName, conf.HTTP.Services); err != nil {
				excludedServices[serviceName] = struct{}{}
				log.FromContext(ctx).Errorf("Invalid service %q configuration: %s", serviceName, err)
				continue
			}

			conf.HTTP.Services[serviceName] = service
		}

		for routerName, router := range configuration.HTTP.Routers {
			if err := checkHTTPRouter(pvd, router, excludedServices, excludedMiddlewares); err != nil {
				log.FromContext(ctx).Errorf("Invalid router %q configuration: %s", routerName, err)
				continue
			}

			conf.HTTP.Routers[routerName] = router
		}

		for serversTransportName, serversTransport := range configuration.HTTP.ServersTransports {
			conf.HTTP.ServersTransports[serversTransportName] = serversTransport
		}
	}

	if configuration.TCP != nil {
		excludedServices := make(map[string]struct{})
		for serviceName, service := range configuration.TCP.Services {
			if err := checkTCPService(pvd, serviceName, conf.TCP.Services); err != nil {
				excludedServices[serviceName] = struct{}{}
				log.FromContext(ctx).Errorf("Invalid TCP service %q configuration: %s", serviceName, err)
				continue
			}

			conf.TCP.Services[serviceName] = service
		}

		for routerName, router := range configuration.TCP.Routers {
			if err := checkTCPRouter(pvd, router, excludedServices); err != nil {
				log.FromContext(ctx).Errorf("Invalid TCP router %q configuration: %s", routerName, err)
				continue
			}

			conf.TCP.Routers[routerName] = router
		}

		for middlewareName, middleware := range configuration.TCP.Middlewares {
			conf.TCP.Middlewares[middlewareName] = middleware
		}
	}

	if configuration.UDP != nil {
		excludedServices := make(map[string]struct{})
		for serviceName, service := range configuration.UDP.Services {
			if err := checkUDPService(pvd, serviceName, conf.UDP.Services); err != nil {
				excludedServices[serviceName] = struct{}{}
				log.FromContext(ctx).Errorf("Invalid UDP service %q configuration: %s", serviceName, err)
				continue
			}

			conf.UDP.Services[serviceName] = service
		}
		for routerName, router := range configuration.UDP.Routers {
			if err := checkUDPRouter(pvd, router, excludedServices); err != nil {
				log.FromContext(ctx).Errorf("Invalid UDP router %q configuration: %s", routerName, err)
				continue
			}

			conf.UDP.Routers[routerName] = router
		}
	}

	if configuration.TLS != nil {
		for _, cert := range configuration.TLS.Certificates {
			conf.TLS.Certificates = append(conf.TLS.Certificates, cert)
		}

		for key, store := range configuration.TLS.Stores {
			conf.TLS.Stores[key] = store
		}

		for tlsOptionsName, options := range configuration.TLS.Options {
			conf.TLS.Options[tlsOptionsName] = options
		}
	}

	return &conf
}

// checkHTTPRouter checks that all resources referenced by the given router are allowed.
func checkHTTPRouter(pvd string, router *dynamic.Router, excludedServices, excludedMiddlewares map[string]struct{}) error {
	if _, excluded := excludedServices[router.Service]; excluded || !isAllowedReference(router.Service, pvd) {
		return fmt.Errorf("service reference not allowed")
	}

	if router.TLS != nil && !isAllowedReference(router.TLS.Options, pvd) {
		return fmt.Errorf("TLS options reference not allowed")
	}

	for _, middlewareName := range router.Middlewares {
		if _, excluded := excludedMiddlewares[middlewareName]; excluded || !isAllowedReference(middlewareName, pvd) {
			return fmt.Errorf("middleware reference not allowed")
		}
	}

	return nil
}

// checkTCPRouter checks that all resources referenced by the given router are allowed.
func checkTCPRouter(pvd string, router *dynamic.TCPRouter, excludedServices map[string]struct{}) error {
	if _, excluded := excludedServices[router.Service]; excluded || !isAllowedReference(router.Service, pvd) {
		return fmt.Errorf("service reference not allowed")
	}

	if router.TLS != nil && !isAllowedReference(router.TLS.Options, pvd) {
		return fmt.Errorf("TLS options reference not allowed")
	}

	return nil
}

// checkUDPRouter checks that all resources referenced by the given router are allowed.
func checkUDPRouter(pvd string, router *dynamic.UDPRouter, excludedServices map[string]struct{}) error {
	if _, excluded := excludedServices[router.Service]; excluded || !isAllowedReference(router.Service, pvd) {
		return fmt.Errorf("service reference not allowed")
	}

	return nil
}

// checkHTTPMiddleware checks that all resources referenced by the given middleware are allowed.
func checkHTTPMiddleware(pvd, middlewareName string, middlewares map[string]*dynamic.Middleware) error {
	if !isAllowedReference(middlewareName, pvd) {
		return fmt.Errorf("middleware reference not allowed: %s", middlewareName)
	}

	parts := strings.Split(middlewareName, "@")
	if len(parts) > 1 && parts[1] != pvd {
		return nil
	}

	middleware, ok := middlewares[parts[0]]
	if !ok {
		return fmt.Errorf("middleware not found: %s", middlewareName)
	}

	if middleware.Chain != nil {
		for _, midName := range middleware.Chain.Middlewares {
			if err := checkHTTPMiddleware(pvd, midName, middlewares); err != nil {
				return fmt.Errorf("chain middleware %q: %w", middlewareName, err)
			}
		}
	}

	if middleware.Errors != nil {
		if !isAllowedReference(middleware.Errors.Service, pvd) {
			return fmt.Errorf("errors middleware service reference not allowed: %s", middleware.Errors.Service)
		}
	}

	return nil
}

// checkHTTPService checks that all resources referenced by the given service are allowed.
func checkHTTPService(pvd, svcName string, services map[string]*dynamic.Service) error {
	if !isAllowedReference(svcName, pvd) {
		return fmt.Errorf("service reference not allowed: %s", svcName)
	}

	// Allowing references from other provider types (e.g. file).
	// This is mandatory because the service will not exist in the map of services.
	// Thus, this does allow tricky references (e.g.: consul > file > consul).
	parts := strings.Split(svcName, "@")
	if len(parts) > 1 && parts[1] != pvd {
		return nil
	}

	service, ok := services[parts[0]]
	if !ok {
		return fmt.Errorf("service not found: %s", svcName)
	}

	if service.LoadBalancer != nil {
		if !isAllowedReference(service.LoadBalancer.ServersTransport, pvd) {
			return fmt.Errorf("serversTransport reference not allowed: %s", service.LoadBalancer.ServersTransport)
		}
	}

	if service.Failover != nil {
		if err := checkHTTPService(pvd, service.Failover.Service, services); err != nil {
			return err
		}

		if err := checkHTTPService(pvd, service.Failover.Fallback, services); err != nil {
			return err
		}
	}

	if service.Weighted != nil {
		for _, wrrService := range service.Weighted.Services {
			if err := checkHTTPService(pvd, wrrService.Name, services); err != nil {
				return err
			}
		}
	}

	if service.Mirroring != nil {
		for _, mirrorService := range service.Mirroring.Mirrors {
			if err := checkHTTPService(pvd, mirrorService.Name, services); err != nil {
				return err
			}
		}
	}

	return nil
}

// checkTCPService checks that all resources referenced by the given service are allowed.
func checkTCPService(pvd, svcName string, services map[string]*dynamic.TCPService) error {
	if !isAllowedReference(svcName, pvd) {
		return fmt.Errorf("service reference not allowed: %s", svcName)
	}

	// Allowing references from other provider types (e.g. file).
	// This is mandatory because the service will not exist in the map of services.
	// Thus, this does allow tricky references (e.g.: consul > file > consul).
	parts := strings.Split(svcName, "@")
	if len(parts) > 1 && parts[1] != pvd {
		return nil
	}

	service, ok := services[parts[0]]
	if !ok {
		return fmt.Errorf("service not found: %s", svcName)
	}

	if service.Weighted != nil {
		for _, wrrService := range service.Weighted.Services {
			if err := checkTCPService(pvd, wrrService.Name, services); err != nil {
				return err
			}
		}
	}

	return nil
}

// checkUDPService checks that all resources referenced by the given service are allowed.
func checkUDPService(pvd, svcName string, services map[string]*dynamic.UDPService) error {
	if !isAllowedReference(svcName, pvd) {
		return fmt.Errorf("service reference not allowed: %s", svcName)
	}

	// Allowing references from other provider types (e.g. file).
	// This is mandatory because the service will not exist in the map of services.
	// Thus, this does allow tricky references (e.g.: consul > file > consul).
	parts := strings.Split(svcName, "@")
	if len(parts) > 1 && parts[1] != pvd {
		return nil
	}

	service, ok := services[parts[0]]
	if !ok {
		return fmt.Errorf("service not found: %s", svcName)
	}

	if service.Weighted != nil {
		for _, wrrService := range service.Weighted.Services {
			if err := checkUDPService(pvd, wrrService.Name, services); err != nil {
				return err
			}
		}
	}

	return nil
}

// isAllowedReference determines whether a cross provider reference is allowed for a named provider.
func isAllowedReference(name, pvd string) bool {
	split := strings.Split(name, "@")
	if len(split) == 1 {
		return true
	}

	pvdName := split[1]

	if !strings.Contains(pvdName, pvd) {
		return true
	}

	return pvdName == pvd
}

// MultiProviderName constructs a unique provider name.
// providerName is the user defined value or the default provider name.
// providerType is the default provider name.
// configurationIndex is the index of the provider in the configuration.
func MultiProviderName(providerName, providerType string, configurationIndex int) string {
	if providerName == providerType {
		return fmt.Sprintf("%s-%d", providerType, configurationIndex)
	}

	return fmt.Sprintf("%s-%s", providerType, providerName)
}
