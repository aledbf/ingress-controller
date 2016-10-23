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

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/apis/extensions"
	"k8s.io/kubernetes/pkg/client/cache"
	clientset "k8s.io/kubernetes/pkg/client/clientset_generated/internalclientset"
	"k8s.io/kubernetes/pkg/client/record"
	client "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/healthz"
	"k8s.io/kubernetes/pkg/runtime"
	"k8s.io/kubernetes/pkg/util/flowcontrol"
	"k8s.io/kubernetes/pkg/util/intstr"
	"k8s.io/kubernetes/pkg/watch"

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
	"github.com/aledbf/ingress-controller/pkg/ingress/status"
	"github.com/aledbf/ingress-controller/pkg/k8s"
	ssl "github.com/aledbf/ingress-controller/pkg/net/ssl"
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

// IController ...
type IController interface {
	Start()
	Stop() error

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

	// controller for NGINX SSL certificates
	secretController *cache.Controller
	secretLister     cache_store.StoreToSSLCertLister

	recorder record.EventRecorder

	syncQueue *task.Queue
	// TaskQueue in charge of keep the secrets referenced from Ingress
	// in sync with the files on disk
	secretQueue *task.Queue

	syncStatus status.Sync

	syncRateLimiter flowcontrol.RateLimiter

	// stopLock is used to enforce only a single call to Stop is active.
	// Needed because we allow stopping through an http endpoint and
	// allowing concurrent stoppers leads to stack traces.
	stopLock *sync.Mutex

	stopCh chan struct{}
}

// Configuration ...
type Configuration struct {
	Client         *client.Client
	ElectionClient *clientset.Clientset

	ResyncPeriod          time.Duration
	DefaultService        string
	IngressClass          string
	Namespace             string
	ConfigMapName         string
	TCPConfigMapName      string
	UDPConfigMapName      string
	DefaultSSLCertificate string
	DefaultHealthzURL     string
	PublishService        string

	Backend ingress.Controller
}

// newIngressController creates an Ingress controller
func newIngressController(config *Configuration) IController {

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
	eventBroadcaster.StartRecordingToSink(config.Client.Events(config.Namespace))

	ic := GenericController{
		cfg:             config,
		stopLock:        &sync.Mutex{},
		stopCh:          make(chan struct{}),
		syncRateLimiter: flowcontrol.NewTokenBucketRateLimiter(0.1, 1),
		recorder: eventBroadcaster.NewRecorder(api.EventSource{
			Component: "ingress-controller",
		}),
	}

	ingEventHandler := cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			addIng := obj.(*extensions.Ingress)
			if !IsValidClass(addIng, config.IngressClass) {
				glog.Infof("Ignoring add for ingress %v based on annotation %v", addIng.Name, ingressClassKey)
				return
			}
			ic.recorder.Eventf(addIng, api.EventTypeNormal, "CREATE", fmt.Sprintf("Ingress %s/%s", addIng.Namespace, addIng.Name))
			ic.syncQueue.Enqueue(obj)
		},
		DeleteFunc: func(obj interface{}) {
			delIng := obj.(*extensions.Ingress)
			if !IsValidClass(delIng, config.IngressClass) {
				glog.Infof("Ignoring add for ingress %v based on annotation %v", delIng.Name, ingressClassKey)
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
			ic.secretQueue.Enqueue(obj)
		},
		DeleteFunc: func(obj interface{}) {
			ic.secretQueue.Enqueue(obj)
		},
		UpdateFunc: func(old, cur interface{}) {
			if !reflect.DeepEqual(old, cur) {
				ic.secretQueue.Enqueue(cur)
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
		&cache.ListWatch{
			ListFunc: func(opts api.ListOptions) (runtime.Object, error) {
				return ic.cfg.Client.Extensions().Ingress(ic.cfg.Namespace).List(opts)
			},
			WatchFunc: func(options api.ListOptions) (watch.Interface, error) {
				return ic.cfg.Client.Extensions().Ingress(ic.cfg.Namespace).Watch(options)
			},
		},
		&extensions.Ingress{}, ic.cfg.ResyncPeriod, ingEventHandler)

	ic.endpLister.Store, ic.endpController = cache.NewInformer(
		&cache.ListWatch{
			ListFunc: func(opts api.ListOptions) (runtime.Object, error) {
				return ic.cfg.Client.Endpoints(ic.cfg.Namespace).List(opts)
			},
			WatchFunc: func(options api.ListOptions) (watch.Interface, error) {
				return ic.cfg.Client.Endpoints(ic.cfg.Namespace).Watch(options)
			},
		},
		&api.Endpoints{}, ic.cfg.ResyncPeriod, eventHandler)

	ic.svcLister.Indexer, ic.svcController = cache.NewIndexerInformer(
		&cache.ListWatch{
			ListFunc: func(opts api.ListOptions) (runtime.Object, error) {
				return ic.cfg.Client.Services(ic.cfg.Namespace).List(opts)
			},
			WatchFunc: func(options api.ListOptions) (watch.Interface, error) {
				return ic.cfg.Client.Services(ic.cfg.Namespace).Watch(options)
			},
		},
		&api.Service{},
		ic.cfg.ResyncPeriod,
		cache.ResourceEventHandlerFuncs{},
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})

	ic.secrLister.Store, ic.secrController = cache.NewInformer(
		&cache.ListWatch{
			ListFunc: func(opts api.ListOptions) (runtime.Object, error) {
				return ic.cfg.Client.Secrets(ic.cfg.Namespace).List(opts)
			},
			WatchFunc: func(options api.ListOptions) (watch.Interface, error) {
				return ic.cfg.Client.Secrets(ic.cfg.Namespace).Watch(options)
			},
		},
		&api.Secret{}, ic.cfg.ResyncPeriod, secrEventHandler)

	ic.mapLister.Store, ic.mapController = cache.NewInformer(
		&cache.ListWatch{
			ListFunc: func(opts api.ListOptions) (runtime.Object, error) {
				return ic.cfg.Client.ConfigMaps(ic.cfg.Namespace).List(opts)
			},
			WatchFunc: func(options api.ListOptions) (watch.Interface, error) {
				return ic.cfg.Client.ConfigMaps(ic.cfg.Namespace).Watch(options)
			},
		},
		&api.ConfigMap{}, ic.cfg.ResyncPeriod, mapEventHandler)

	ic.secretLister.Store, ic.secretController = cache.NewInformer(
		&cache.ListWatch{},
		&ingress.SSLCert{},
		ic.cfg.ResyncPeriod,
		cache.ResourceEventHandlerFuncs{},
	)

	ic.syncStatus = status.NewStatusSyncer(status.Config{
		Client:         config.Client,
		ElectionClient: ic.cfg.ElectionClient,
		PublishService: ic.cfg.PublishService,
		IngressLister:  ic.ingLister,
	})

	ic.syncQueue = task.NewTaskQueue(ic.sync)

	ic.secretQueue = task.NewTaskQueue(ic.syncSecret)

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

func (ic *GenericController) getConfigMap(ns, name string) (*api.ConfigMap, error) {
	// TODO: check why ic.mapLister.Store.GetByKey(mapKey) is not stable (random content)
	return ic.cfg.Client.ConfigMaps(ns).Get(name)
}

func (ic *GenericController) sync(key interface{}) error {
	ic.syncRateLimiter.Accept()

	if ic.syncQueue.IsShuttingDown() {
		return nil
	}

	if !ic.controllersInSync() {
		time.Sleep(podStoreSyncedPollPeriod)
		return fmt.Errorf("deferring sync till endpoints controller has synced")
	}

	// by default no custom configuration configmap
	cfg := &api.ConfigMap{}

	if ic.cfg.ConfigMapName != "" {
		// Search for custom configmap (defined in main args)
		var err error
		ns, name, _ := k8s.ParseNameNS(ic.cfg.ConfigMapName)
		cfg, err = ic.getConfigMap(ns, name)
		if err != nil {
			return fmt.Errorf("unexpected error searching configmap %v: %v", ic.cfg.ConfigMapName, err)
		}
	}

	ings := ic.ingLister.Store.List()
	upstreams, servers := ic.getUpstreamServers(ings)

	data, err := ic.cfg.Backend.OnUpdate(cfg, ingress.Configuration{
		HealthzURL:   ic.cfg.DefaultHealthzURL,
		Upstreams:    upstreams,
		Servers:      servers,
		TCPUpstreams: ic.getTCPServices(),
		UDPUpstreams: ic.getUDPServices(),
	})
	if err != nil {
		return err
	}

	if !ic.cfg.Backend.IsReloadRequired(data) {
		return nil
	}
	out, err := ic.cfg.Backend.Restart(data)
	if err != nil {
		glog.Errorf("unexpected failure restarting the backend: \n%v", string(out))
		return err
	}
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
		glog.V(3).Infof("no configured tcp services found: %v", err)
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
		if k == "80" || k == "443" || k == "8181" || k == "18080" {
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

// getUpstreamServers returns a list of Upstream and Server to be used by the backend
// An upstream can be used in multiple servers if the namespace, service name and port are the same
func (ic *GenericController) getUpstreamServers(data []interface{}) ([]*ingress.Upstream, []*ingress.Server) {
	upstreams := ic.createUpstreams(data)
	servers := ic.createServers(data, upstreams)

	upsDefaults := ic.cfg.Backend.UpstreamDefaults()

	for _, ingIf := range data {
		ing := ingIf.(*extensions.Ingress)

		nginxAuth, err := auth.ParseAnnotations(ic.cfg.Client, ing, auth.DefAuthDirectory)
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
		glog.V(3).Infof("auth request annotation: %v", ra)
		if err != nil {
			glog.V(5).Infof("error reading auth request annotation in Ingress %v/%v: %v", ing.GetNamespace(), ing.GetName(), err)
		}

		prx := proxy.ParseAnnotations(upsDefaults, ing)
		glog.V(5).Infof("proxy timeouts annotation: %v", prx)

		certAuth, err := authtls.ParseAnnotations(ing, ic.getAuthCertificate)
		glog.V(5).Infof("auth request annotation: %v", certAuth)
		if err != nil {
			glog.V(3).Infof("error reading certificate auth annotation in Ingress %v/%v: %v", ing.GetNamespace(), ing.GetName(), err)
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

			if rule.HTTP == nil && host != defServerName {
				// no rules, host is not default server.
				// check if Ingress rules contains Backend and replace default backend
				defBackend := fmt.Sprintf("default-backend-%v-%v-%v", ing.GetNamespace(), ing.Spec.Backend.ServiceName, ing.Spec.Backend.ServicePort.String())
				ups := upstreams[defBackend]
				for _, loc := range server.Locations {
					loc.Upstream = *ups
				}
				continue
			}

			for _, path := range rule.HTTP.Paths {
				upsName := fmt.Sprintf("%v-%v-%v", ing.GetNamespace(), path.Backend.ServiceName, path.Backend.ServicePort.String())
				ups := upstreams[upsName]

				// we need to check if the upstream contains the default backend
				if isDefaultUpstream(ups) && ing.Spec.Backend != nil {
					defBackend := fmt.Sprintf("default-backend-%v-%v-%v", ing.GetNamespace(), ing.Spec.Backend.ServiceName, ing.Spec.Backend.ServicePort.String())
					if defUps, ok := upstreams[defBackend]; ok {
						ups = defUps
					}
				}

				nginxPath := path.Path
				// if there's no path defined we assume /
				// in NGINX / == /*
				if nginxPath == "" {
					ic.recorder.Eventf(ing, api.EventTypeWarning, "MAPPING",
						"Ingress rule '%v/%v' contains no path definition. Assuming /",
						ing.GetNamespace(), ing.GetName())
					nginxPath = rootLocation
				}

				// Validate that there is no previous rule for the same host and path.
				addLoc := true
				for _, loc := range server.Locations {
					if loc.Path == rootLocation && nginxPath == rootLocation && loc.IsDefBackend {
						loc.Upstream = *ups
						loc.BasicDigestAuth = *nginxAuth
						loc.RateLimit = *rl
						loc.Redirect = *locRew
						loc.SecureUpstream = secUpstream
						loc.Whitelist = *wl
						loc.IsDefBackend = false
						loc.Upstream = *ups
						loc.EnableCORS = eCORS
						loc.ExternalAuth = ra
						loc.Proxy = *prx
						loc.CertificateAuth = *certAuth

						addLoc = false
						continue
					}

					if loc.Path == nginxPath {
						ic.recorder.Eventf(ing, api.EventTypeWarning, "MAPPING",
							"Path '%v' already defined in another Ingress rule", nginxPath)
						addLoc = false
						break
					}
				}

				if addLoc {
					server.Locations = append(server.Locations, &ingress.Location{
						Path:            nginxPath,
						Upstream:        *ups,
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
			glog.Warningf("upstream %v does not have any active endpoints. Using default backend", value.Name)
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
	cert, err := ic.getPemCertificate(secretName)
	if err != nil {
		return &authtls.SSLCert{}, err
	}

	return &authtls.SSLCert{
		CertFileName: cert.CertFileName,
		KeyFileName:  cert.KeyFileName,
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
			defBackend = fmt.Sprintf("default-backend-%v-%v-%v", ing.GetNamespace(), ing.Spec.Backend.ServiceName, ing.Spec.Backend.ServicePort.String())
			glog.V(3).Infof("creating upstream %v", defBackend)
			upstreams[defBackend] = newUpstream(defBackend)

			svcKey := fmt.Sprintf("%v/%v", ing.GetNamespace(), ing.Spec.Backend.ServiceName)
			endps, err := ic.getSvcEndpoints(svcKey, ing.Spec.Backend.ServicePort.String(), hz)
			upstreams[defBackend].Backends = append(upstreams[defBackend].Backends, endps...)
			if err != nil {
				glog.Warningf("error creating upstream %v: %v", defBackend, err)
			}
		}

		for _, rule := range ing.Spec.Rules {
			if rule.IngressRuleValue.HTTP == nil {
				continue
			}

			for _, path := range rule.HTTP.Paths {
				name := fmt.Sprintf("%v-%v-%v", ing.GetNamespace(), path.Backend.ServiceName, path.Backend.ServicePort.String())
				if _, ok := upstreams[name]; ok {
					continue
				}

				glog.V(3).Infof("creating upstream %v", name)
				upstreams[name] = newUpstream(name)

				svcKey := fmt.Sprintf("%v/%v", ing.GetNamespace(), path.Backend.ServiceName)
				endp, err := ic.getSvcEndpoints(svcKey, path.Backend.ServicePort.String(), hz)
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

func (ic *GenericController) getSvcEndpoints(svcKey, backendPort string,
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

	pems := ic.getPemsFromIngress(data)

	var ngxCert ingress.SSLCert
	var err error

	if ic.cfg.DefaultSSLCertificate == "" {
		// use system certificated generated at image build time
		cert, key := ssl.GetFakeSSLCert()
		ngxCert, err = ssl.AddOrUpdateCertAndKey("system-snake-oil-certificate", cert, key, "")
	} else {
		ngxCert, err = ic.getPemCertificate(ic.cfg.DefaultSSLCertificate)
	}

	if err == nil {
		pems[defServerName] = ngxCert
		servers[defServerName].SSL = true
		servers[defServerName].SSLCertificate = ngxCert.PemFileName
		servers[defServerName].SSLCertificateKey = ngxCert.PemFileName
		servers[defServerName].SSLPemChecksum = ngxCert.PemSHA
	} else {
		glog.Warningf("unexpected error reading default SSL certificate: %v", err)
	}

	ngxProxy := *proxy.ParseAnnotations(ic.cfg.Backend.UpstreamDefaults(), nil)

	locs := []*ingress.Location{}
	locs = append(locs, &ingress.Location{
		Path:         rootLocation,
		IsDefBackend: true,
		Upstream:     *ic.getDefaultUpstream(),
		Proxy:        ngxProxy,
	})
	servers[defServerName] = &ingress.Server{Name: defServerName, Locations: locs}

	for _, ingIf := range data {
		ing := ingIf.(*extensions.Ingress)

		for _, rule := range ing.Spec.Rules {
			host := rule.Host
			if host == "" {
				host = defServerName
			}

			if _, ok := servers[host]; ok {
				glog.V(3).Infof("rule %v/%v uses a host already defined. Skipping server creation", ing.GetNamespace(), ing.GetName())
			} else {
				locs := []*ingress.Location{}
				loc := &ingress.Location{
					Path:         rootLocation,
					IsDefBackend: true,
					Upstream:     *ic.getDefaultUpstream(),
					Proxy:        ngxProxy,
				}

				if ing.Spec.Backend != nil {
					defUpstream := fmt.Sprintf("default-backend-%v-%v-%v", ing.GetNamespace(), ing.Spec.Backend.ServiceName, ing.Spec.Backend.ServicePort.String())
					if backendUpstream, ok := upstreams[defUpstream]; ok {
						if host == "" || host == defServerName {
							ic.recorder.Eventf(ing, api.EventTypeWarning, "MAPPING", "error: rules with Spec.Backend are allowed with hostnames")
						} else {
							loc.Upstream = *backendUpstream
						}
					}
				}

				locs = append(locs, loc)
				servers[host] = &ingress.Server{Name: host, Locations: locs}
			}

			if ngxCert, ok := pems[host]; ok {
				server := servers[host]
				server.SSL = true
				server.SSLCertificate = ngxCert.PemFileName
				server.SSLCertificateKey = ngxCert.PemFileName
				server.SSLPemChecksum = ngxCert.PemSHA
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
				} else {
					glog.Warningf("error mapping service port: %v", err)
					err := ic.checkSvcForUpdate(s)
					if err != nil {
						glog.Warningf("error mapping service ports: %v", err)
						continue
					}

					port, err := service.GetPortMapping(servicePort.StrVal, s)
					if err == nil {
						targetPort = port
					}
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

	go ic.syncQueue.Run(time.Second, ic.stopCh)
	go ic.secretQueue.Run(time.Second, ic.stopCh)

	go ic.syncStatus.Run(ic.stopCh)

	<-ic.stopCh
}
