package controller

import (
	"flag"
	"fmt"
	"net/http"
	"net/http/pprof"
	"os"
	"time"

	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/spf13/pflag"

	"github.com/aledbf/ingress-controller/pkg/ingress"
	"github.com/aledbf/ingress-controller/pkg/k8s"

	"k8s.io/kubernetes/pkg/api"
	clientset "k8s.io/kubernetes/pkg/client/clientset_generated/internalclientset"
	"k8s.io/kubernetes/pkg/client/restclient"
	"k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/healthz"
	kubectl_util "k8s.io/kubernetes/pkg/kubectl/cmd/util"
)

// NewIngressController returns a configured Ingress controller ready to start
func NewIngressController(backend ingress.Controller) Interface {
	var (
		flags = pflag.NewFlagSet("", pflag.ExitOnError)

		defaultSvc = flags.String("default-backend-service", "",
			`Service used to serve a 404 page for the default backend. Takes the form
    	namespace/name. The controller uses the first node port of this Service for
    	the default backend.`)

		ingressClass = flags.String("ingress-class", "nginx",
			`Name of the ingress class to route through this controller.`)

		nxgConfigMap = flags.String("config-map", "",
			`Name of the ConfigMap that containes the custom configuration to use`)

		publishSvc = flags.String("publish-service", "",
			`Service fronting the ingress controllers. Takes the form
 		namespace/name. The controller will set the endpoint records on the
 		ingress objects to reflect those on the service.`)

		tcpConfigMapName = flags.String("tcp-services-configmap", "",
			`Name of the ConfigMap that containes the definition of the TCP services to expose.
		The key in the map indicates the external port to be used. The value is the name of the
		service with the format namespace/serviceName and the port of the service could be a 
		number of the name of the port.
		The ports 80 and 443 are not allowed as external ports. This ports are reserved for nginx`)

		udpConfigMapName = flags.String("udp-services-configmap", "",
			`Name of the ConfigMap that containes the definition of the UDP services to expose.
		The key in the map indicates the external port to be used. The value is the name of the
		service with the format namespace/serviceName and the port of the service could be a 
		number of the name of the port.`)

		resyncPeriod = flags.Duration("sync-period", 60*time.Second,
			`Relist and confirm cloud resources this often.`)

		watchNamespace = flags.String("watch-namespace", api.NamespaceAll,
			`Namespace to watch for Ingress. Default is to watch all namespaces`)

		healthzPort = flags.Int("healthz-port", 10254, "port for healthz endpoint.")

		profiling = flags.Bool("profiling", true, `Enable profiling via web interface host:port/debug/pprof/`)

		defSSLCertificate = flags.String("default-ssl-certificate", "", `Name of the secret 
		that contains a SSL certificate to be used as default for a HTTPS catch-all server`)

		defHealthzURL = flags.String("health-check-path", "/healthz", `Defines 
		the URL to be used as health check inside in the default server in NGINX.`)
	)

	flags.AddGoFlagSet(flag.CommandLine)
	flags.Parse(os.Args)
	clientConfig := kubectl_util.DefaultClientConfig(flags)

	flag.Set("logtostderr", "true")

	//glog.Infof("Using build version %v from repo %v commit %v", version.RELEASE, version.REPO, version.COMMIT)
	if *ingressClass != "" {
		glog.Infof("Watching for ingress class: %s", *ingressClass)
	}

	if *defaultSvc == "" {
		glog.Fatalf("Please specify --default-backend-service")
	}

	kubeconfig, err := restclient.InClusterConfig()
	if err != nil {
		kubeconfig, err = clientConfig.ClientConfig()
		if err != nil {
			glog.Fatalf("error configuring the client: %v", err)
		}
	}

	kubeClient, err := unversioned.New(kubeconfig)
	if err != nil {
		glog.Fatalf("failed to create client: %v", err)
	}

	leaderElectionClient, err := clientset.NewForConfig(restclient.AddUserAgent(kubeconfig, "leader-election"))
	if err != nil {
		glog.Fatalf("vinvalid API configuration: %v", err)
	}

	_, err = k8s.IsValidService(kubeClient, *defaultSvc)
	if err != nil {
		glog.Fatalf("no service with name %v found: %v", *defaultSvc, err)
	}
	glog.Infof("validated %v as the default backend", *defaultSvc)

	if *publishSvc != "" {
		svc, err := k8s.IsValidService(kubeClient, *publishSvc)
		if err != nil {
			glog.Fatalf("no service with name %v found: %v", *publishSvc, err)
		}

		if len(svc.Status.LoadBalancer.Ingress) == 0 {
			// We could poll here, but we instead just exit and rely on k8s to restart us
			glog.Fatalf("service %s does not (yet) have ingress points", *publishSvc)
		}

		glog.Infof("service %v validated as source of Ingress status", *publishSvc)
	}

	if *nxgConfigMap != "" {
		_, _, err = k8s.ParseNameNS(*nxgConfigMap)
		if err != nil {
			glog.Fatalf("configmap error: %v", err)
		}
	}

	os.MkdirAll(ingress.DefaultSSLDirectory, 0655)

	config := &Configuration{
		Client:                kubeClient,
		ElectionClient:        leaderElectionClient,
		ResyncPeriod:          *resyncPeriod,
		DefaultService:        *defaultSvc,
		IngressClass:          *ingressClass,
		Namespace:             *watchNamespace,
		ConfigMapName:         *nxgConfigMap,
		TCPConfigMapName:      *tcpConfigMapName,
		UDPConfigMapName:      *udpConfigMapName,
		DefaultSSLCertificate: *defSSLCertificate,
		DefaultHealthzURL:     *defHealthzURL,
		PublishService:        *publishSvc,
		Backend:               backend,
	}

	ic := newIngressController(config)
	go registerHandlers(*profiling, *healthzPort, ic)
	return ic
}

func registerHandlers(enableProfiling bool, port int, ic Interface) {
	mux := http.NewServeMux()
	healthz.InstallHandler(mux, ic)

	mux.Handle("/metrics", prometheus.Handler())

	mux.HandleFunc("/build", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		//fmt.Fprintf(w, "build version %v from repo %v commit %v", version.RELEASE, version.REPO, version.COMMIT)
	})

	mux.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		ic.Stop()
	})

	if enableProfiling {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}

	server := &http.Server{
		Addr:    fmt.Sprintf(":%v", port),
		Handler: mux,
	}
	glog.Fatal(server.ListenAndServe())
}
