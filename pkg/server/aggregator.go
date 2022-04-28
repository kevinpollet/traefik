package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-acme/lego/v4/challenge/tlsalpn01"
	"github.com/traefik/traefik/v2/pkg/config/dynamic"
	"github.com/traefik/traefik/v2/pkg/log"
	"github.com/traefik/traefik/v2/pkg/server/provider"
	"github.com/traefik/traefik/v2/pkg/tls"
)

func mergeConfiguration(configurations dynamic.Configurations, defaultEntryPoints []string) dynamic.Configuration {
	// TODO: see if we can use DeepCopies inside, so that the given argument is left
	// untouched, and the modified copy is returned.
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

	var defaultTLSOptionProviders []string
	var defaultTLSStoreProviders []string
	for pvd, configuration := range configurations {
		if configuration.HTTP != nil {
			for routerName, router := range configuration.HTTP.Routers {
				if len(router.EntryPoints) == 0 {
					log.WithoutContext().
						WithField(log.RouterName, routerName).
						Debugf("No entryPoint defined for this router, using the default one(s) instead: %+v", defaultEntryPoints)
					router.EntryPoints = defaultEntryPoints
				}

				conf.HTTP.Routers[provider.MakeQualifiedName(pvd, routerName)] = router
			}
			for middlewareName, middleware := range configuration.HTTP.Middlewares {
				conf.HTTP.Middlewares[provider.MakeQualifiedName(pvd, middlewareName)] = middleware
			}
			for serviceName, service := range configuration.HTTP.Services {
				conf.HTTP.Services[provider.MakeQualifiedName(pvd, serviceName)] = service
			}
			for modelName, model := range configuration.HTTP.Models {
				conf.HTTP.Models[provider.MakeQualifiedName(pvd, modelName)] = model
			}
			for serversTransportName, serversTransport := range configuration.HTTP.ServersTransports {
				conf.HTTP.ServersTransports[provider.MakeQualifiedName(pvd, serversTransportName)] = serversTransport
			}
		}

		if configuration.TCP != nil {
			for routerName, router := range configuration.TCP.Routers {
				if len(router.EntryPoints) == 0 {
					log.WithoutContext().
						WithField(log.RouterName, routerName).
						Debugf("No entryPoint defined for this TCP router, using the default one(s) instead: %+v", defaultEntryPoints)
					router.EntryPoints = defaultEntryPoints
				}
				conf.TCP.Routers[provider.MakeQualifiedName(pvd, routerName)] = router
			}
			for middlewareName, middleware := range configuration.TCP.Middlewares {
				conf.TCP.Middlewares[provider.MakeQualifiedName(pvd, middlewareName)] = middleware
			}
			for serviceName, service := range configuration.TCP.Services {
				conf.TCP.Services[provider.MakeQualifiedName(pvd, serviceName)] = service
			}
		}

		if configuration.UDP != nil {
			for routerName, router := range configuration.UDP.Routers {
				conf.UDP.Routers[provider.MakeQualifiedName(pvd, routerName)] = router
			}
			for serviceName, service := range configuration.UDP.Services {
				conf.UDP.Services[provider.MakeQualifiedName(pvd, serviceName)] = service
			}
		}

		if configuration.TLS != nil {
			for _, cert := range configuration.TLS.Certificates {
				if containsACMETLS1(cert.Stores) && pvd != "tlsalpn.acme" {
					continue
				}

				conf.TLS.Certificates = append(conf.TLS.Certificates, cert)
			}

			for key, store := range configuration.TLS.Stores {
				if key != tls.DefaultTLSStoreName {
					key = provider.MakeQualifiedName(pvd, key)
				} else {
					defaultTLSStoreProviders = append(defaultTLSStoreProviders, pvd)
				}
				conf.TLS.Stores[key] = store
			}

			for tlsOptionsName, options := range configuration.TLS.Options {
				if tlsOptionsName != "default" {
					tlsOptionsName = provider.MakeQualifiedName(pvd, tlsOptionsName)
				} else {
					defaultTLSOptionProviders = append(defaultTLSOptionProviders, pvd)
				}

				conf.TLS.Options[tlsOptionsName] = options
			}
		}
	}

	if len(defaultTLSStoreProviders) > 1 {
		log.WithoutContext().Errorf("Default TLS Stores defined multiple times in %v", defaultTLSOptionProviders)
		delete(conf.TLS.Stores, tls.DefaultTLSStoreName)
	}

	if len(defaultTLSOptionProviders) == 0 {
		conf.TLS.Options[tls.DefaultTLSConfigName] = tls.DefaultTLSOptions
	} else if len(defaultTLSOptionProviders) > 1 {
		log.WithoutContext().Errorf("Default TLS Options defined multiple times in %v", defaultTLSOptionProviders)
		// We do not set an empty tls.TLS{} as above so that we actually get a "cascading failure" later on,
		// i.e. routers depending on this missing TLS option will fail to initialize as well.
		delete(conf.TLS.Options, tls.DefaultTLSConfigName)
	}

	return conf
}

func applyModel(cfg dynamic.Configuration) dynamic.Configuration {
	if cfg.HTTP == nil || len(cfg.HTTP.Models) == 0 {
		return cfg
	}

	rts := make(map[string]*dynamic.Router)

	for name, rt := range cfg.HTTP.Routers {
		router := rt.DeepCopy()

		eps := router.EntryPoints
		router.EntryPoints = nil

		for _, epName := range eps {
			m, ok := cfg.HTTP.Models[epName+"@internal"]
			if ok {
				cp := router.DeepCopy()

				cp.EntryPoints = []string{epName}

				if cp.TLS == nil {
					cp.TLS = m.TLS
				}

				cp.Middlewares = append(m.Middlewares, cp.Middlewares...)

				rtName := name
				if len(eps) > 1 {
					rtName = epName + "-" + name
				}
				rts[rtName] = cp
			} else {
				router.EntryPoints = append(router.EntryPoints, epName)

				rts[name] = router
			}
		}
	}

	cfg.HTTP.Routers = rts

	return cfg
}

func containsACMETLS1(stores []string) bool {
	for _, store := range stores {
		if store == tlsalpn01.ACMETLS1Protocol {
			return true
		}
	}

	return false
}

// sanitizeReferences removes disallowed cross provider references.
// TODO handle copy of models ?
func sanitizeReferences(pvd string, configuration dynamic.Configuration) dynamic.Configuration {
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
			if err := checkMiddleware(pvd, middlewareName, conf.HTTP.Middlewares); err != nil {
				excludedMiddlewares[middlewareName] = struct{}{}
				log.FromContext(ctx).Errorf("Invalid middleware %q configuration: %s", middlewareName, err)
				continue
			}

			conf.HTTP.Middlewares[middlewareName] = middleware
		}

		excludedServices := make(map[string]struct{})
		for serviceName, service := range configuration.HTTP.Services {
			if err := checkService(pvd, serviceName, conf.HTTP.Services); err != nil {
				excludedServices[serviceName] = struct{}{}
				log.FromContext(ctx).Errorf("Invalid service %q configuration: %s", serviceName, err)
				continue
			}

			conf.HTTP.Services[serviceName] = service
		}

		for routerName, router := range configuration.HTTP.Routers {
			if err := checkRouter(pvd, router, excludedServices, excludedMiddlewares); err != nil {
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

	return conf
}

// checkRouter checks that all resources referenced by the given router are allowed.
func checkRouter(pvd string, router *dynamic.Router, excludedServices, excludedMiddlewares map[string]struct{}) error {
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

// checkMiddleware checks that all resources referenced by the given middleware are allowed.
func checkMiddleware(pvd, middlewareName string, middlewares map[string]*dynamic.Middleware) error {
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
			if err := checkMiddleware(pvd, midName, middlewares); err != nil {
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

// checkService checks that all resources referenced by the given service are allowed.
func checkService(pvd, svcName string, services map[string]*dynamic.Service) error {
	if !isAllowedReference(svcName, pvd) {
		return fmt.Errorf("service reference not allowed: %s", svcName)
	}

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
		err := checkService(pvd, service.Failover.Service, services)
		if err != nil {
			return err
		}

		err = checkService(pvd, service.Failover.Fallback, services)
		if err != nil {
			return err
		}
	}

	if service.Weighted != nil {
		for _, wrrService := range service.Weighted.Services {
			err := checkService(pvd, wrrService.Name, services)
			if err != nil {
				return err
			}
		}
	}

	if service.Mirroring != nil {
		for _, mirrorService := range service.Mirroring.Mirrors {
			err := checkService(pvd, mirrorService.Name, services)
			if err != nil {
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
			err := checkTCPService(pvd, wrrService.Name, services)
			if err != nil {
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
			err := checkUDPService(pvd, wrrService.Name, services)
			if err != nil {
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
