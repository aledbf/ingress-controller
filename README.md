
[![Build Status](https://travis-ci.org/aledbf/ingress-controller.svg?branch=master)](https://travis-ci.org/aledbf/ingress-controller)
[![Coverage Status](https://coveralls.io/repos/github/aledbf/ingress-controller/badge.svg?branch=master)](https://coveralls.io/github/aledbf/ingress-controller?branch=master)
[![Go Report Card](https://goreportcard.com/badge/github.com/aledbf/ingress-controller)](https://goreportcard.com/report/github.com/aledbf/ingress-controller)

# Ingress Controller

This project contains the boilerplate to create an Ingress controller in order to avoid starting from scrach.

See [Ingress controller documentation](https://github.com/kubernetes/contrib/blob/master/ingress/controllers/README.md) for details on how it works.


### Backends
 - [NGINX](https://github.com/aledbf/ingress-controller/blob/master/backends/nginx)


### How I can use X as backend?

Chek if the backend is available [here](https://github.com/aledbf/ingress-controller/blob/master/backends)

Create a [main.go](https://github.com/aledbf/ingress-controller/blob/master/backends/nginx/pkg/cmd/controller/main.go) file 
```
func main() {
	// start a new nginx controller
	ngx := newNGINXController()
	// create a custom Ingress controller using NGINX as backend
	ic := controller.NewIngressController(ngx)
	// start the controller
	ic.Start()
	// wait
	glog.Infof("shutting down Ingress controller...")
}
```

Implement the [Controller](https://github.com/aledbf/ingress-controller/blob/master/pkg/ingress/types.go#L40) interface
```
type Controller interface {
	// Start returns the command is executed to start the backend.
	// The command must run in foreground.
	Start()
	// Stop stops the backend
	Stop() error
	// Restart reload the backend with the a configuration file returning
	// the combined output of Stdout and Stderr
	Restart(data []byte) ([]byte, error)
	// Tests returns a commands that checks if the configuration file is valid
	// Example: nginx -t -c <file>
	Test(file string) *exec.Cmd
	// OnUpdate callback invoked from the sync queue https://github.com/aledbf/ingress-controller/blob/master/pkg/ingress/controller/controller.go#L355
	// when an update occurs. This is executed frequently because Ingress
	// controllers watches changes in:
	// - Ingresses: main work
	// - Secrets: referenced from Ingress rules with TLS configured
	// - ConfigMaps: where the controller reads custom configuration
	// - Services: referenced from Ingress rules and required to obtain
	//	 information about ports and annotations
	// - Endpoints: referenced from Services and what the backend uses
	//	 to route traffic
	//
	// ConfigMap content of --configmap
	// Configuration returns the translation from Ingress rules containing
	// information about all the upstreams (service endpoints ) "virtual"
	// servers (FQDN)
	// and all the locations inside each server. Each location contains
	// information about all the annotations were configured
	// https://github.com/aledbf/ingress-controller/blob/master/pkg/ingress/types.go#L48
	OnUpdate(*api.ConfigMap, Configuration) ([]byte, error)
	// UpstreamDefaults returns the minimum settings required to configure the
	// communication to upstream servers (endpoints)
	UpstreamDefaults() defaults.Backend
	// IsReloadRequired checks if the backend must be reloaded or not.
	// The parameter contains the new rendered template
	IsReloadRequired([]byte) bool
	// Info returns information about the ingress controller
	// This can include build version, repository, etc.
	Info() string
}
```
