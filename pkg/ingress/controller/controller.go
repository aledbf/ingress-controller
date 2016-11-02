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

package controller

import (
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang/glog"

	"k8s.io/kubernetes/pkg/healthz"

	"k8s.io/client-go/kubernetes"
	types "k8s.io/client-go/pkg/api"
	api "k8s.io/client-go/pkg/api/v1"
	extensions "k8s.io/client-go/pkg/apis/extensions/v1beta1"
	"k8s.io/client-go/pkg/fields"
	"k8s.io/client-go/pkg/util/flowcontrol"
	"k8s.io/client-go/pkg/util/intstr"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	cache_store "github.com/aledbf/ingress-controller/pkg/cache"
	"github.com/aledbf/ingress-controller/pkg/ingress"
	"github.com/aledbf/ingress-controller/pkg/ingress/annotations/auth"
	"github.com/aledbf/ingress-controller/pkg/ingress/annotations/authreq"
	"github.com/aledbf/ingress-controller/pkg/ingress/annotations/authtls"
	"github.com/aledbf/ingress-controller/pkg/ingress/annotations/cors"
	"github.com/aledbf/ingress-controller/pkg/ingress/annotations/healthcheck"
	"github.com/aledbf/ingress-controller/pkg/ingress/annotations/ipwhitelist"
	"github.com/aledbf/ingress-controller/pkg/ingress/annotations/proxy"
	"github.com/aledbf/ingress-controller/pkg/ingress/annotations/ratelimit"
	"github.com/aledbf/ingress-controller/pkg/ingress/annotations/rewrite"
	"github.com/aledbf/ingress-controller/pkg/ingress/annotations/secureupstream"
	"github.com/aledbf/ingress-controller/pkg/ingress/annotations/service"
	"github.com/aledbf/ingress-controller/pkg/ingress/annotations/sslpassthrough"
	"github.com/aledbf/ingress-controller/pkg/ingress/status"
	"github.com/aledbf/ingress-controller/pkg/k8s"
	local_strings "github.com/aledbf/ingress-controller/pkg/strings"
	"github.com/aledbf/ingress-controller/pkg/task"
)

const (
	defUpstreamName          = "upstream-default-backend"
	defServerName            = "_"
	podStoreSyncedPollPeriod = 1 * time.Second
	rootLocation             = "/"

	// ingressClassKey picks a specific "class" for the Ingress. The controller
	// only processes Ingresses with this annotation either unset, or set
	// to either the configured value or the empty string.
	ingressClassKey = "kubernetes.io/ingress.class"
)

var (
	// list of ports that cannot be used by TCP or UDP services
	reservedPorts = []string{"80", "443", "8181", "18080"}
)

// Interface holds the methods to handle an Ingress backend
type Interface interface {
	Start()
	Stop() error

	Info() string

	healthz.HealthzChecker
}

// GenericController watches the kubernetes api and adds/removes services from the loadbalancer
type GenericController struct {
	healthz.HealthzChecker

	cfg *Configuration

	ingController  *cache.Controller
	endpController *cache.Controller
	svcController  *cache.Controller
	secrController *cache.Controller
	mapController  *cache.Controller

	ingLister  cache_store.StoreToIngressLister
	svcLister  cache.StoreToServiceLister
	endpLister cache.StoreToEndpointsLister
	secrLister cache_store.StoreToSecretsLister
	mapLister  cache_store.StoreToConfigmapLister

	recorder record.EventRecorder

	syncQueue *task.Queue

	syncStatus status.Sync

	// controller for SSL certificates
	sslCertTracker *sslCertTracker
	// TaskQueue in charge of keep the secrets referenced from Ingress
	// in sync with the files on disk
	secretQueue *task.Queue

	syncRateLimiter flowcontrol.RateLimiter

	// stopLock is used to enforce only a single call to Stop is active.
	// Needed because we allow stopping through an http endpoint and
	// allowing concurrent stoppers leads to stack traces.
	stopLock *sync.Mutex

	stopCh chan struct{}
}

// Configuration contains all the settings required by an Ingress controller
type Configuration struct {
	Client         kubernetes.Interface
	ElectionClient *kubernetes.Clientset

	ResyncPeriod   time.Duration
	DefaultService string
	IngressClass   string
	Namespace      string
	ConfigMapName  string
	// optional
	TCPConfigMapName string
	// optional
	UDPConfigMapName      string
	DefaultSSLCertificate string
	DefaultHealthzURL     string
	// optional
	PublishService string

	Backend ingress.Controller
}

// newIngressController creates an Ingress controller
func newIngressController(config *Configuration) Interface {

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
	eventBroadcaster.StartRecordingToSink(config.Client.Core().Events(config.Namespace))

	ic := GenericController{
		cfg:             config,
		stopLock:        &sync.Mutex{},
		stopCh:          make(chan struct{}),
		syncRateLimiter: flowcontrol.NewTokenBucketRateLimiter(0.1, 1),
		recorder: eventBroadcaster.NewRecorder(api.EventSource{
			Component: "ingress-controller",
		}),
		sslCertTracker: newSSLCertTracker(),
	}

	ic.syncQueue = task.NewTaskQueue(ic.sync)
	ic.secretQueue = task.NewTaskQueue(ic.syncSecret)

	// from here to the end of the method all the code is just boilerplate
	// required to watch Ingress, Secrets, ConfigMaps and Endoints.
	// This is used to detect new content, updates or removals and act accordingly
	ingEventHandler := cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			addIng := obj.(*extensions.Ingress)
			if !IsValidClass(addIng, config.IngressClass) {
				glog.Infof("ignoring add for ingress %v based on annotation %v", addIng.Name, ingressClassKey)
				return
			}
			ic.recorder.Eventf(addIng, api.EventTypeNormal, "CREATE", fmt.Sprintf("Ingress %s/%s", addIng.Namespace, addIng.Name))
			ic.syncQueue.Enqueue(obj)
		},
		DeleteFunc: func(obj interface{}) {
			delIng := obj.(*extensions.Ingress)
			if !IsValidClass(delIng, config.IngressClass) {
				glog.Infof("ignoring add for ingress %v based on annotation %v", delIng.Name, ingressClassKey)
				return
			}
			ic.recorder.Eventf(delIng, api.EventTypeNormal, "DELETE", fmt.Sprintf("Ingress %s/%s", delIng.Namespace, delIng.Name))
			ic.syncQueue.Enqueue(obj)
		},
		UpdateFunc: func(old, cur interface{}) {
			curIng := cur.(*extensions.Ingress)
			if !IsValidClass(curIng, config.IngressClass) {
				return
			}

			if !reflect.DeepEqual(old, cur) {
				upIng := cur.(*extensions.Ingress)
				ic.recorder.Eventf(upIng, api.EventTypeNormal, "UPDATE", fmt.Sprintf("Ingress %s/%s", upIng.Namespace, upIng.Name))
				ic.syncQueue.Enqueue(cur)
			}
		},
	}

	secrEventHandler := cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			sec := obj.(*api.Secret)
			ic.secretQueue.Enqueue(sec)
		},
		DeleteFunc: func(obj interface{}) {
			sec := obj.(*api.Secret)
			ic.sslCertTracker.Delete(fmt.Sprintf("%v/%v", sec.Namespace, sec.Name))
		},
		UpdateFunc: func(old, cur interface{}) {
			if !reflect.DeepEqual(old, cur) {
				sec := cur.(*api.Secret)
				ic.secretQueue.Enqueue(sec)
			}
		},
	}

	eventHandler := cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			ic.syncQueue.Enqueue(obj)
		},
		DeleteFunc: func(obj interface{}) {
			ic.syncQueue.Enqueue(obj)
		},
		UpdateFunc: func(old, cur interface{}) {
			if !reflect.DeepEqual(old, cur) {
				ic.syncQueue.Enqueue(cur)
			}
		},
	}

	mapEventHandler := cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(old, cur interface{}) {
			if !reflect.DeepEqual(old, cur) {
				upCmap := cur.(*api.ConfigMap)
				mapKey := fmt.Sprintf("%s/%s", upCmap.Namespace, upCmap.Name)
				// updates to configuration configmaps can trigger an update
				if mapKey == ic.cfg.ConfigMapName || mapKey == ic.cfg.TCPConfigMapName || mapKey == ic.cfg.UDPConfigMapName {
					ic.recorder.Eventf(upCmap, api.EventTypeNormal, "UPDATE", fmt.Sprintf("ConfigMap %v", mapKey))
					ic.syncQueue.Enqueue(cur)
				}
			}
		},
	}

	ic.ingLister.Store, ic.ingController = cache.NewInformer(
		cache.NewListWatchFromClient(ic.cfg.Client.Core().RESTClient(), "ingress", ic.cfg.Namespace, fields.Everything()),
		&extensions.Ingress{}, ic.cfg.ResyncPeriod, ingEventHandler)

	ic.endpLister.Store, ic.endpController = cache.NewInformer(
		cache.NewListWatchFromClient(ic.cfg.Client.Core().RESTClient(), "endpoint", ic.cfg.Namespace, fields.Everything()),
		&api.Endpoints{}, ic.cfg.ResyncPeriod, eventHandler)

	ic.svcLister.Indexer, ic.svcController = cache.NewIndexerInformer(
		cache.NewListWatchFromClient(ic.cfg.Client.Core().RESTClient(), "service", ic.cfg.Namespace, fields.Everything()),
		&api.Service{},
		ic.cfg.ResyncPeriod,
		cache.ResourceEventHandlerFuncs{},
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})

	ic.secrLister.Store, ic.secrController = cache.NewInformer(
		cache.NewListWatchFromClient(ic.cfg.Client.Core().RESTClient(), "secret", ic.cfg.Namespace, fields.Everything()),
		&api.Secret{}, ic.cfg.ResyncPeriod, secrEventHandler)

	ic.mapLister.Store, ic.mapController = cache.NewInformer(
		cache.NewListWatchFromClient(ic.cfg.Client.Core().RESTClient(), "configmap", ic.cfg.Namespace, fields.Everything()),
		&api.ConfigMap{}, ic.cfg.ResyncPeriod, mapEventHandler)

	ic.syncStatus = status.NewStatusSyncer(status.Config{
		Client:         config.Client,
		ElectionClient: ic.cfg.ElectionClient,
		PublishService: ic.cfg.PublishService,
		IngressLister:  ic.ingLister,
	})

	return ic
}

func (ic *GenericController) controllersInSync() bool {
	return ic.ingController.HasSynced() &&
		ic.svcController.HasSynced() &&
		ic.endpController.HasSynced() &&
		ic.secrController.HasSynced() &&
		ic.mapController.HasSynced()
}

// Name returns the healthcheck name
func (ic GenericController) Name() string {
	return "Ingress Controller"
}

// Check returns if the nginx healthz endpoint is returning ok (status code 200)
func (ic GenericController) Check(_ *http.Request) error {
	res, err := http.Get("http://127.0.0.1:18080/healthz")
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("Ingress controller is not healthy")
	}
	return nil
}

// Info returns information about the backend
func (ic GenericController) Info() string {
	return ic.cfg.Backend.Info()
}

// IngressClass returns information about the backend
func (ic GenericController) IngressClass() string {
	return ic.cfg.IngressClass
}

// getSecret searchs for a secret in the local secrets Store
func (ic *GenericController) getSecret(name string) (*types.Secret, error) {
	s, exists, err := ic.secrLister.Store.GetByKey(name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("secret %v was not found", name)
	}
	return s.(*types.Secret), nil
}

func (ic *GenericController) getConfigMap(ns, name string) (*api.ConfigMap, error) {
	// TODO: check why ic.mapLister.Store.GetByKey(mapKey) is not stable (random content)
	return ic.cfg.Client.Core().ConfigMaps(ns).Get(name)
}

// sync collects all the pieces required to assemble the configuration file and
// then sends the content to the backend (OnUpdate) receiving the populated
// template as response reloading the backend if is required.
func (ic *GenericController) sync(key interface{}) error {
	ic.syncRateLimiter.Accept()

	if ic.syncQueue.IsShuttingDown() {
		return nil
	}

	if !ic.controllersInSync() {
		time.Sleep(podStoreSyncedPollPeriod)
		return fmt.Errorf("deferring sync till endpoints controller has synced")
	}

	// by default no custom configuration
	cfg := &api.ConfigMap{}

	if ic.cfg.ConfigMapName != "" {
		// search for custom configmap (defined in main args)
		var err error
		ns, name, _ := k8s.ParseNameNS(ic.cfg.ConfigMapName)
		cfg, err = ic.getConfigMap(ns, name)
		if err != nil {
			// requeue
			return fmt.Errorf("unexpected error searching configmap %v: %v", ic.cfg.ConfigMapName, err)
		}
	}

	upstreams, servers := ic.getUpstreamServers()
	var passUpstreams []*ingress.SSLPassthroughUpstreams
	for _, server := range servers {
		if !server.SSLPassthrough {
			continue
		}

		for _, loc := range server.Locations {
			if loc.Path != rootLocation {
				continue
			}
			passUpstreams = append(passUpstreams, &ingress.SSLPassthroughUpstreams{
				Upstream: loc.Upstream,
				Host:     server.Name,
			})
			break
		}
	}

	data, err := ic.cfg.Backend.OnUpdate(cfg, ingress.Configuration{
		HealthzURL:           ic.cfg.DefaultHealthzURL,
		Upstreams:            upstreams,
		Servers:              servers,
		TCPUpstreams:         ic.getTCPServices(),
		UDPUpstreams:         ic.getUDPServices(),
		PassthroughUpstreams: passUpstreams,
	})
	if err != nil {
		return err
	}

	if !ic.cfg.Backend.IsReloadRequired(data) {
		return nil
	}

	glog.Infof("reloading ingress backend...")
	out, err := ic.cfg.Backend.Restart(data)
	if err != nil {
		incReloadErrorCount()
		glog.Errorf("unexpected failure restarting the backend: \n%v", string(out))
		return err
	}
	glog.Infof("ingress backend successfully reloaded...")
	incReloadCount()
	return nil
}

func (ic *GenericController) getTCPServices() []*ingress.Location {
	if ic.cfg.TCPConfigMapName == "" {
		// no configmap for TCP services
		return []*ingress.Location{}
	}

	ns, name, err := k8s.ParseNameNS(ic.cfg.TCPConfigMapName)
	if err != nil {
		glog.Warningf("%v", err)
		return []*ingress.Location{}
	}
	tcpMap, err := ic.getConfigMap(ns, name)
	if err != nil {
		glog.V(5).Infof("no configured tcp services found: %v", err)
		return []*ingress.Location{}
	}

	return ic.getStreamServices(tcpMap.Data, api.ProtocolTCP)
}

func (ic *GenericController) getUDPServices() []*ingress.Location {
	if ic.cfg.UDPConfigMapName == "" {
		// no configmap for TCP services
		return []*ingress.Location{}
	}

	ns, name, err := k8s.ParseNameNS(ic.cfg.UDPConfigMapName)
	if err != nil {
		glog.Warningf("%v", err)
		return []*ingress.Location{}
	}
	tcpMap, err := ic.getConfigMap(ns, name)
	if err != nil {
		glog.V(3).Infof("no configured tcp services found: %v", err)
		return []*ingress.Location{}
	}

	return ic.getStreamServices(tcpMap.Data, api.ProtocolUDP)
}

func (ic *GenericController) getStreamServices(data map[string]string, proto api.Protocol) []*ingress.Location {
	var svcs []*ingress.Location
	// k -> port to expose
	// v -> <namespace>/<service name>:<port from service to be used>
	for k, v := range data {
		port, err := strconv.Atoi(k)
		if err != nil {
			glog.Warningf("%v is not valid as a TCP port", k)
			continue
		}

		// this ports used by the backend
		if local_strings.StringInSlice(k, reservedPorts) {
			glog.Warningf("port %v cannot be used for TCP or UDP services. It is reserved for the Ingress controller", k)
			continue
		}

		nsSvcPort := strings.Split(v, ":")
		if len(nsSvcPort) != 2 {
			glog.Warningf("invalid format (namespace/name:port) '%v'", k)
			continue
		}

		nsName := nsSvcPort[0]
		svcPort := nsSvcPort[1]

		svcNs, svcName, err := k8s.ParseNameNS(nsName)
		if err != nil {
			glog.Warningf("%v", err)
			continue
		}

		svcObj, svcExists, err := ic.svcLister.Indexer.GetByKey(nsName)
		if err != nil {
			glog.Warningf("error getting service %v: %v", nsName, err)
			continue
		}

		if !svcExists {
			glog.Warningf("service %v was not found", nsName)
			continue
		}

		svc := svcObj.(*api.Service)

		var endps []ingress.UpstreamServer
		targetPort, err := strconv.Atoi(svcPort)
		if err != nil {
			for _, sp := range svc.Spec.Ports {
				if sp.Name == svcPort {
					endps = ic.getEndpoints(svc, sp.TargetPort, proto, &healthcheck.Upstream{})
					break
				}
			}
		} else {
			// we need to use the TargetPort (where the endpoints are running)
			for _, sp := range svc.Spec.Ports {
				if sp.Port == int32(targetPort) {
					endps = ic.getEndpoints(svc, sp.TargetPort, proto, &healthcheck.Upstream{})
					break
				}
			}
		}

		sort.Sort(ingress.UpstreamServerByAddrPort(endps))

		// tcp upstreams cannot contain empty upstreams and there is no
		// default backend equivalent for TCP
		if len(endps) == 0 {
			glog.Warningf("service %v/%v does not have any active endpoints", svcNs, svcName)
			continue
		}

		svcs = append(svcs, &ingress.Location{
			Path: k,
			Upstream: ingress.Upstream{
				Name:     fmt.Sprintf("%v-%v-%v", svcNs, svcName, port),
				Backends: endps,
			},
		})
	}

	return svcs
}

// getDefaultUpstream returns an upstream associated with the
// default backend service. In case of error retrieving information
// configure the upstream to return http code 503.
func (ic *GenericController) getDefaultUpstream() *ingress.Upstream {
	upstream := &ingress.Upstream{
		Name: defUpstreamName,
	}
	svcKey := ic.cfg.DefaultService
	svcObj, svcExists, err := ic.svcLister.Indexer.GetByKey(svcKey)
	if err != nil {
		glog.Warningf("unexpected error searching the default backend %v: %v", ic.cfg.DefaultService, err)
		upstream.Backends = append(upstream.Backends, newDefaultServer())
		return upstream
	}

	if !svcExists {
		glog.Warningf("service %v does not exists", svcKey)
		upstream.Backends = append(upstream.Backends, newDefaultServer())
		return upstream
	}

	svc := svcObj.(*api.Service)
	endps := ic.getEndpoints(svc, svc.Spec.Ports[0].TargetPort, api.ProtocolTCP, &healthcheck.Upstream{})
	if len(endps) == 0 {
		glog.Warningf("service %v does not have any active endpoints", svcKey)
		endps = []ingress.UpstreamServer{newDefaultServer()}
	}

	upstream.Backends = append(upstream.Backends, endps...)
	return upstream
}

type ingressByRevision []interface{}

func (c ingressByRevision) Len() int      { return len(c) }
func (c ingressByRevision) Swap(i, j int) { c[i], c[j] = c[j], c[i] }
func (c ingressByRevision) Less(i, j int) bool {
	ir := c[i].(*extensions.Ingress).ResourceVersion
	jr := c[j].(*extensions.Ingress).ResourceVersion
	return ir < jr
}

// getUpstreamServers returns a list of Upstream and Server to be used by the backend
// An upstream can be used in multiple servers if the namespace, service name and port are the same
func (ic *GenericController) getUpstreamServers() ([]*ingress.Upstream, []*ingress.Server) {
	ings := ic.ingLister.Store.List()
	sort.Sort(ingressByRevision(ings))

	upstreams := ic.createUpstreams(ings)
	servers := ic.createServers(ings, upstreams)

	upsDefaults := ic.cfg.Backend.UpstreamDefaults()

	for _, ingIf := range ings {
		ing := ingIf.(*extensions.Ingress)

		nginxAuth, err := auth.ParseAnnotations(ing, auth.DefAuthDirectory, ic.getSecret)
		glog.V(5).Infof("auth annotation: %v", nginxAuth)
		if err != nil {
			glog.V(5).Infof("error reading authentication in Ingress %v/%v: %v", ing.GetNamespace(), ing.GetName(), err)
		}

		rl, err := ratelimit.ParseAnnotations(ing)
		glog.V(5).Infof("rate limit annotation: %v", rl)
		if err != nil {
			glog.V(5).Infof("error reading rate limit annotation in Ingress %v/%v: %v", ing.GetNamespace(), ing.GetName(), err)
		}

		secUpstream, err := secureupstream.ParseAnnotations(ing)
		if err != nil {
			glog.V(5).Infof("error reading secure upstream in Ingress %v/%v: %v", ing.GetNamespace(), ing.GetName(), err)
		}

		locRew, err := rewrite.ParseAnnotations(upsDefaults, ing)
		if err != nil {
			glog.V(5).Infof("error parsing rewrite annotations for Ingress rule %v/%v: %v", ing.GetNamespace(), ing.GetName(), err)
		}

		wl, err := ipwhitelist.ParseAnnotations(upsDefaults, ing)
		glog.V(5).Infof("white list annotation: %v", wl)
		if err != nil {
			glog.V(5).Infof("error reading white list annotation in Ingress %v/%v: %v", ing.GetNamespace(), ing.GetName(), err)
		}

		eCORS, err := cors.ParseAnnotations(ing)
		if err != nil {
			glog.V(5).Infof("error reading CORS annotation in Ingress %v/%v: %v", ing.GetNamespace(), ing.GetName(), err)
		}

		ra, err := authreq.ParseAnnotations(ing)
		glog.V(5).Infof("auth request annotation: %v", ra)
		if err != nil {
			glog.V(5).Infof("error reading auth request annotation in Ingress %v/%v: %v", ing.GetNamespace(), ing.GetName(), err)
		}

		prx := proxy.ParseAnnotations(upsDefaults, ing)
		glog.V(5).Infof("proxy timeouts annotation: %v", prx)

		certAuth, err := authtls.ParseAnnotations(ing, ic.getAuthCertificate)
		glog.V(5).Infof("auth request annotation: %v", certAuth)
		if err != nil {
			glog.V(5).Infof("error reading certificate auth annotation in Ingress %v/%v: %v", ing.GetNamespace(), ing.GetName(), err)
		}

		for _, rule := range ing.Spec.Rules {
			host := rule.Host
			if host == "" {
				host = defServerName
			}
			server := servers[host]
			if server == nil {
				server = servers[defServerName]
			}

			// use default upstream
			defBackend := upstreams[defUpstreamName]
			// we need to check if the spec contains the default backend
			if ing.Spec.Backend != nil {
				glog.V(3).Infof("ingress rule %v/%v defines a default Backend %v/%v",
					ing.Namespace,
					ing.Name,
					ing.Spec.Backend.ServiceName,
					ing.Spec.Backend.ServicePort.String())

				name := fmt.Sprintf("%v-%v-%v",
					ing.GetNamespace(),
					ing.Spec.Backend.ServiceName,
					ing.Spec.Backend.ServicePort.String())

				if defUps, ok := upstreams[name]; ok {
					defBackend = defUps
				}
			}

			if rule.HTTP == nil &&
				len(ing.Spec.TLS) == 0 &&
				host != defServerName {
				glog.V(3).Infof("ingress rule %v/%v does not contains HTTP or TLS rules. using default backend", ing.Namespace, ing.Name)
				server.Locations[0].Upstream = *defBackend
				continue
			}

			for _, path := range rule.HTTP.Paths {
				upsName := fmt.Sprintf("%v-%v-%v",
					ing.GetNamespace(),
					path.Backend.ServiceName,
					path.Backend.ServicePort.String())

				ups := upstreams[upsName]

				// if there's no path defined we assume /
				nginxPath := rootLocation
				if path.Path != "" {
					nginxPath = path.Path
				}

				addLoc := true
				for _, loc := range server.Locations {
					if loc.Path == nginxPath {
						addLoc = false

						if !loc.IsDefBackend {
							glog.V(3).Infof("avoiding replacement of ingress rule %v/%v location %v upstream %v (%v)", ing.Namespace, ing.Name, loc.Path, ups.Name, loc.Upstream.Name)
							break
						}

						glog.V(3).Infof("replacing ingress rule %v/%v location %v upstream %v (%v)", ing.Namespace, ing.Name, loc.Path, ups.Name, loc.Upstream.Name)
						loc.Upstream = *ups
						loc.IsDefBackend = false
						loc.BasicDigestAuth = *nginxAuth
						loc.RateLimit = *rl
						loc.Redirect = *locRew
						loc.SecureUpstream = secUpstream
						loc.Whitelist = *wl
						loc.Upstream = *ups
						loc.EnableCORS = eCORS
						loc.ExternalAuth = ra
						loc.Proxy = *prx
						loc.CertificateAuth = *certAuth
						break
					}
				}
				// is a new location
				if addLoc {
					glog.V(3).Infof("adding location %v in ingress rule %v/%v upstream", nginxPath, ing.Namespace, ing.Name, ups.Name)
					server.Locations = append(server.Locations, &ingress.Location{
						Path:            nginxPath,
						Upstream:        *ups,
						IsDefBackend:    false,
						BasicDigestAuth: *nginxAuth,
						RateLimit:       *rl,
						Redirect:        *locRew,
						SecureUpstream:  secUpstream,
						Whitelist:       *wl,
						EnableCORS:      eCORS,
						ExternalAuth:    ra,
						Proxy:           *prx,
						CertificateAuth: *certAuth,
					})
				}
			}
		}
	}

	// TODO: find a way to make this more readable
	// The structs must be ordered to always generate the same file
	// if the content does not change.
	aUpstreams := make([]*ingress.Upstream, 0, len(upstreams))
	for _, value := range upstreams {
		if len(value.Backends) == 0 {
			glog.V(3).Infof("upstream %v does not have any active endpoints. Using default backend", value.Name)
			value.Backends = append(value.Backends, newDefaultServer())
		}
		sort.Sort(ingress.UpstreamServerByAddrPort(value.Backends))
		aUpstreams = append(aUpstreams, value)
	}
	sort.Sort(ingress.UpstreamByNameServers(aUpstreams))

	aServers := make([]*ingress.Server, 0, len(servers))
	for _, value := range servers {
		sort.Sort(ingress.LocationByPath(value.Locations))
		aServers = append(aServers, value)
	}
	sort.Sort(ingress.ServerByName(aServers))

	return aUpstreams, aServers
}

func (ic *GenericController) getAuthCertificate(secretName string) (*authtls.SSLCert, error) {
	bc, exists := ic.sslCertTracker.Get(secretName)
	if !exists {
		return &authtls.SSLCert{}, fmt.Errorf("secret %v does not exists", secretName)
	}
	cert := bc.(*ingress.SSLCert)
	return &authtls.SSLCert{
		Secret:       secretName,
		CertFileName: cert.PemFileName,
		CAFileName:   cert.CAFileName,
		PemSHA:       cert.PemSHA,
	}, nil
}

// createUpstreams creates the NGINX upstreams for each service referenced in
// Ingress rules. The servers inside the upstream are endpoints.
func (ic *GenericController) createUpstreams(data []interface{}) map[string]*ingress.Upstream {
	upstreams := make(map[string]*ingress.Upstream)
	upstreams[defUpstreamName] = ic.getDefaultUpstream()

	upsDefaults := ic.cfg.Backend.UpstreamDefaults()
	for _, ingIf := range data {
		ing := ingIf.(*extensions.Ingress)

		hz := healthcheck.ParseAnnotations(upsDefaults, ing)

		var defBackend string
		if ing.Spec.Backend != nil {
			defBackend = fmt.Sprintf("%v-%v-%v",
				ing.GetNamespace(),
				ing.Spec.Backend.ServiceName,
				ing.Spec.Backend.ServicePort.String())

			glog.V(3).Infof("creating upstream %v", defBackend)
			upstreams[defBackend] = newUpstream(defBackend)

			svcKey := fmt.Sprintf("%v/%v", ing.GetNamespace(), ing.Spec.Backend.ServiceName)
			endps, err := ic.serviceEndpoints(svcKey, ing.Spec.Backend.ServicePort.String(), hz)
			upstreams[defBackend].Backends = append(upstreams[defBackend].Backends, endps...)
			if err != nil {
				glog.Warningf("error creating upstream %v: %v", defBackend, err)
			}
		}

		for _, rule := range ing.Spec.Rules {
			if rule.HTTP == nil {
				continue
			}

			for _, path := range rule.HTTP.Paths {
				name := fmt.Sprintf("%v-%v-%v",
					ing.GetNamespace(),
					path.Backend.ServiceName,
					path.Backend.ServicePort.String())

				if _, ok := upstreams[name]; ok {
					continue
				}

				glog.V(3).Infof("creating upstream %v", name)
				upstreams[name] = newUpstream(name)

				svcKey := fmt.Sprintf("%v/%v", ing.GetNamespace(), path.Backend.ServiceName)
				endp, err := ic.serviceEndpoints(svcKey, path.Backend.ServicePort.String(), hz)
				if err != nil {
					glog.Warningf("error obtaining service endpoints: %v", err)
					continue
				}
				upstreams[name].Backends = endp
			}
		}
	}

	return upstreams
}

// serviceEndpoints returns the upstream servers (endpoints) associated
// to a service.
func (ic *GenericController) serviceEndpoints(svcKey, backendPort string,
	hz *healthcheck.Upstream) ([]ingress.UpstreamServer, error) {
	svcObj, svcExists, err := ic.svcLister.Indexer.GetByKey(svcKey)

	var upstreams []ingress.UpstreamServer
	if err != nil {
		return upstreams, fmt.Errorf("error getting service %v from the cache: %v", svcKey, err)
	}

	if !svcExists {
		err = fmt.Errorf("service %v does not exists", svcKey)
		return upstreams, err
	}

	svc := svcObj.(*api.Service)
	glog.V(3).Infof("obtaining port information for service %v", svcKey)
	for _, servicePort := range svc.Spec.Ports {
		// targetPort could be a string, use the name or the port (int)
		if strconv.Itoa(int(servicePort.Port)) == backendPort ||
			servicePort.TargetPort.String() == backendPort ||
			servicePort.Name == backendPort {

			endps := ic.getEndpoints(svc, servicePort.TargetPort, api.ProtocolTCP, hz)
			if len(endps) == 0 {
				glog.Warningf("service %v does not have any active endpoints", svcKey)
			}

			upstreams = append(upstreams, endps...)
			break
		}
	}

	return upstreams, nil
}

func (ic *GenericController) createServers(data []interface{}, upstreams map[string]*ingress.Upstream) map[string]*ingress.Server {
	servers := make(map[string]*ingress.Server)
	ngxProxy := *proxy.ParseAnnotations(ic.cfg.Backend.UpstreamDefaults(), nil)

	upsDefaults := ic.cfg.Backend.UpstreamDefaults()

	// default server
	servers[defServerName] = &ingress.Server{
		Name: defServerName,
		Locations: []*ingress.Location{
			{
				Path:         rootLocation,
				IsDefBackend: true,
				Upstream:     *ic.getDefaultUpstream(),
				Proxy:        ngxProxy,
			},
		}}

	// initialize all the servers
	for _, ingIf := range data {
		ing := ingIf.(*extensions.Ingress)
		// check if ssl passthrough is configured
		sslpt, err := sslpassthrough.ParseAnnotations(upsDefaults, ing)
		if err != nil {
			glog.V(5).Infof("error reading ssl passthrough annotation in Ingress %v/%v: %v", ing.GetNamespace(), ing.GetName(), err)
		}

		for _, rule := range ing.Spec.Rules {
			host := rule.Host
			if host == "" {
				host = defServerName
			}
			if _, ok := servers[host]; ok {
				// server already configured
				continue
			}
			servers[host] = &ingress.Server{
				Name: host,
				Locations: []*ingress.Location{
					{
						Path:         rootLocation,
						IsDefBackend: true,
						Upstream:     *ic.getDefaultUpstream(),
						Proxy:        ngxProxy,
					},
				}, SSLPassthrough: sslpt}
		}
	}

	// configure default location and SSL
	for _, ingIf := range data {
		ing := ingIf.(*extensions.Ingress)

		for _, rule := range ing.Spec.Rules {
			host := rule.Host
			if host == "" {
				host = defServerName
			}

			// only add certificate if the server does not have one previously configured
			if len(ing.Spec.TLS) > 0 && !servers[host].SSL {
				key := fmt.Sprintf("%v/%v", ing.Namespace, ing.Spec.TLS[0].SecretName)
				bc, exists := ic.sslCertTracker.Get(key)
				if exists {
					cert := bc.(*ingress.SSLCert)
					if isHostValid(host, cert) {
						servers[host].SSL = true
						servers[host].SSLCertificate = cert.PemFileName
						//servers[host].SSLCertificateKey = cert.PemFileName
						servers[host].SSLPemChecksum = cert.PemSHA
					}
				}
			}

			if ing.Spec.Backend != nil {
				defUpstream := fmt.Sprintf("%v-%v-%v", ing.GetNamespace(), ing.Spec.Backend.ServiceName, ing.Spec.Backend.ServicePort.String())
				if backendUpstream, ok := upstreams[defUpstream]; ok {
					if host == "" || host == defServerName {
						ic.recorder.Eventf(ing, api.EventTypeWarning, "MAPPING", "error: rules with Spec.Backend are allowed only with hostnames")
						continue
					}
					servers[host].Locations[0].Upstream = *backendUpstream
				}
			}
		}
	}

	return servers
}

// getEndpoints returns a list of <endpoint ip>:<port> for a given service/target port combination.
func (ic *GenericController) getEndpoints(
	s *api.Service,
	servicePort intstr.IntOrString,
	proto api.Protocol,
	hz *healthcheck.Upstream) []ingress.UpstreamServer {
	glog.V(3).Infof("getting endpoints for service %v/%v and port %v", s.Namespace, s.Name, servicePort.String())
	ep, err := ic.endpLister.GetServiceEndpoints(s)
	if err != nil {
		glog.Warningf("unexpected error obtaining service endpoints: %v", err)
		return []ingress.UpstreamServer{}
	}

	upsServers := []ingress.UpstreamServer{}

	for _, ss := range ep.Subsets {
		for _, epPort := range ss.Ports {

			if !reflect.DeepEqual(epPort.Protocol, proto) {
				continue
			}

			var targetPort int32

			switch servicePort.Type {
			case intstr.Int:
				if int(epPort.Port) == servicePort.IntValue() {
					targetPort = epPort.Port
				}
			case intstr.String:
				port, err := service.GetPortMapping(servicePort.StrVal, s)
				if err == nil {
					targetPort = port
					continue
				}

				glog.Warningf("error mapping service port: %v", err)
				err = ic.checkSvcForUpdate(s)
				if err != nil {
					glog.Warningf("error mapping service ports: %v", err)
					continue
				}

				port, err = service.GetPortMapping(servicePort.StrVal, s)
				if err == nil {
					targetPort = port
				}
			}

			// check for invalid port value
			if targetPort == -1 {
				continue
			}

			for _, epAddress := range ss.Addresses {
				ups := ingress.UpstreamServer{
					Address:     epAddress.IP,
					Port:        fmt.Sprintf("%v", targetPort),
					MaxFails:    hz.MaxFails,
					FailTimeout: hz.FailTimeout,
				}
				upsServers = append(upsServers, ups)
			}
		}
	}

	glog.V(3).Infof("endpoints found: %v", upsServers)
	return upsServers
}

// Stop stops the loadbalancer controller.
func (ic GenericController) Stop() error {
	ic.stopLock.Lock()
	defer ic.stopLock.Unlock()

	// Only try draining the workqueue if we haven't already.
	if !ic.syncQueue.IsShuttingDown() {
		glog.Infof("shutting down controller queues")
		close(ic.stopCh)
		go ic.syncQueue.Shutdown()
		go ic.secretQueue.Shutdown()
		ic.syncStatus.Shutdown()
		return nil
	}

	return fmt.Errorf("shutdown already in progress")
}

// Start starts the Ingress controller.
func (ic GenericController) Start() {
	glog.Infof("starting Ingress controller")
	go ic.cfg.Backend.Start()

	go ic.ingController.Run(ic.stopCh)
	go ic.endpController.Run(ic.stopCh)
	go ic.svcController.Run(ic.stopCh)
	go ic.secrController.Run(ic.stopCh)
	go ic.mapController.Run(ic.stopCh)

	go ic.secretQueue.Run(time.Second, ic.stopCh)
	go ic.syncQueue.Run(time.Second, ic.stopCh)

	go ic.syncStatus.Run(ic.stopCh)

	<-ic.stopCh
}
