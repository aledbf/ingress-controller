/*
Copyright 2015 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package ingress

import (
	"os/exec"

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/healthz"

	"github.com/aledbf/ingress-controller/pkg/ingress/annotations/auth"
	"github.com/aledbf/ingress-controller/pkg/ingress/annotations/authreq"
	"github.com/aledbf/ingress-controller/pkg/ingress/annotations/authtls"
	"github.com/aledbf/ingress-controller/pkg/ingress/annotations/ipwhitelist"
	"github.com/aledbf/ingress-controller/pkg/ingress/annotations/proxy"
	"github.com/aledbf/ingress-controller/pkg/ingress/annotations/ratelimit"
	"github.com/aledbf/ingress-controller/pkg/ingress/annotations/rewrite"
)

// IngressController ...
type IngressController interface {
	// Ingress controller must provide a health checker
	healthz.HealthzChecker
	// Start returns the command is executed to start the backend.
	// The command must run in foreground.
	Start() *exec.Cmd
	// Stop stops the backend
	Stop() error
	// Restart returns the command required to reload the backend.
	// Basically the command that re-reads the configuration file.
	// (usually send a HUP signal the process)
	Restart() *exec.Cmd
	// Tests returns a commands that checks if the configuration file is valid
	// Example: nginx -t -c <file>
	Test(file string) *exec.Cmd
	// OnUpdate callback invoked from the sync queue https://github.com/aledbf/ingress-controller/blob/master/pkg/ingress/controller/controller.go#L355
	// when an update occurs. This is executed frequently because an Ingress controllers watches changes in:
	// - Ingresses: main work
	// - Secrets: referenced from Ingress rules with TLS configured
	// - ConfigMaps: where the controller reads custom configuration
	// - Services: referenced from Ingress rules and required to obtain information about ports and annotations
	// - Endpoints: referenced from Services and what the backend uses to route traffic
	//
	// ConfigMap content of --config-map
	// Configuration returns the translation from Ingress rules containing information about all the upstreams (service endpoints ) "virtual" servers (FQDN)
	// and all the locations inside each server. Each location contains information about all the annotations were configured
	// https://github.com/aledbf/ingress-controller/blob/master/pkg/ingress/types.go#L48
	OnUpdate(*api.ConfigMap, Configuration) error
}

// Configuration describes
type Configuration struct {
	Upstreams    []*Upstream
	Servers      []*Server
	TCPUpstreams []*Location
	UDPUpstreams []*Location
}

// Upstream describes an upstream server (endpoint)
type Upstream struct {
	// Name represents an unique api.Service name formatted
	// as <namespace>-<name>-<port>
	Name string
	// Backends
	Backends []UpstreamServer
	// Secure indicates if the communication with the en
	Secure bool
}

// UpstreamByNameServers sorts upstreams by name
type UpstreamByNameServers []*Upstream

func (c UpstreamByNameServers) Len() int      { return len(c) }
func (c UpstreamByNameServers) Swap(i, j int) { c[i], c[j] = c[j], c[i] }
func (c UpstreamByNameServers) Less(i, j int) bool {
	return c[i].Name < c[j].Name
}

// UpstreamServer describes a server in an upstream
type UpstreamServer struct {
	// Address IP address of the endpoint
	Address string
	Port    string
	// MaxFails returns the maximum number of check failures
	// allowed before this should be considered dow.
	// Setting 0 indicates that the check is performed by a Kubernetes probe
	MaxFails    int
	FailTimeout int
}

// Server describes a virtual server
type Server struct {
	Name              string
	Locations         []*Location
	SSL               bool
	SSLCertificate    string
	SSLCertificateKey string
	SSLPemChecksum    string
}

// Location describes a server location
type Location struct {
	Path            string
	IsDefBackend    bool
	Upstream        Upstream
	BasicDigestAuth auth.BasicDigest
	RateLimit       ratelimit.RateLimit
	Redirect        rewrite.Redirect
	SecureUpstream  bool
	Whitelist       ipwhitelist.SourceRange
	EnableCORS      bool
	ExternalAuth    authreq.External
	Proxy           proxy.Configuration
	CertificateAuth authtls.SSLCert
}

// UpstreamServerByAddrPort sorts upstream servers by address and port
type UpstreamServerByAddrPort []UpstreamServer

func (c UpstreamServerByAddrPort) Len() int      { return len(c) }
func (c UpstreamServerByAddrPort) Swap(i, j int) { c[i], c[j] = c[j], c[i] }
func (c UpstreamServerByAddrPort) Less(i, j int) bool {
	iName := c[i].Address
	jName := c[j].Address
	if iName != jName {
		return iName < jName
	}

	iU := c[i].Port
	jU := c[j].Port
	return iU < jU
}

// ServerByName sorts server by name
type ServerByName []*Server

func (c ServerByName) Len() int      { return len(c) }
func (c ServerByName) Swap(i, j int) { c[i], c[j] = c[j], c[i] }
func (c ServerByName) Less(i, j int) bool {
	return c[i].Name < c[j].Name
}

// LocationByPath sorts location by path
// Location / is the last one
type LocationByPath []*Location

func (c LocationByPath) Len() int      { return len(c) }
func (c LocationByPath) Swap(i, j int) { c[i], c[j] = c[j], c[i] }
func (c LocationByPath) Less(i, j int) bool {
	return c[i].Path > c[j].Path
}

// SSLCert describes a SSL certificate to be used in a server
type SSLCert struct {
	CertFileName string
	KeyFileName  string
	CAFileName   string

	// PemFileName contains the path to the file with the certificate and key concatenated
	PemFileName string
	// PemSHA contains the sha1 of the pem file.
	// This is used to detect changes in the secret that contains the certificates
	PemSHA string
	// CN contains all the common names defined in the SSL certificate
	CN []string
}
