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

package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"

	"github.com/golang/glog"

	"github.com/aledbf/ingress-controller/pkg/ingress"

	ngx_template "github.com/aledbf/ingress-controller/backends/nginx/pkg/template"

	"k8s.io/kubernetes/pkg/api"
)

var (
	tmplPath = "/etc/nginx/template/nginx.tmpl"
	cfgPath  = "/etc/nginx/nginx.conf"
	binary   = "/usr/sbin/nginx"
)

func newNGINXController() ingress.IngressController {
	ngx := os.Getenv("NGINX_BINARY")
	if ngx == "" {
		ngx = binary
	}
	n := NGINXController{binary: ngx}

	var onChange func()
	onChange = func() {
		template, err := ngx_template.NewTemplate(tmplPath, onChange)
		if err != nil {
			glog.Warningf("invalid template: %v", err)
			return
		}

		n.t.Close()
		n.t = template
		glog.Info("new NGINX template loaded")
	}

	ngxTpl, err := ngx_template.NewTemplate(tmplPath, onChange)
	if err != nil {
		glog.Fatalf("invalid NGINX template: %v", err)
	}

	n.t = ngxTpl
	return n
}

// NGINXController ...
type NGINXController struct {
	t      *ngx_template.Template
	binary string
}

// Start ...
func (n NGINXController) Start() *exec.Cmd {
	return exec.Command(n.binary, "-c", cfgPath)
}

// Stop ...
func (n NGINXController) Stop() error {
	n.t.Close()
	return exec.Command(n.binary, "-s", "stop").Run()
}

// Restart ...
func (n NGINXController) Restart() *exec.Cmd {
	return exec.Command(n.binary, "-s", "reload")
}

// Test ...
func (n NGINXController) Test(file string) *exec.Cmd {
	return exec.Command(n.binary, "-t", "-c", file)
}

// testTemplate checks if the NGINX configuration inside the byte array is valid
// running the command "nginx -t" using a temporal file.
func (n NGINXController) testTemplate(cfg []byte) error {
	tmpfile, err := ioutil.TempFile("", "nginx-cfg")
	if err != nil {
		return err
	}
	defer tmpfile.Close()
	ioutil.WriteFile(tmpfile.Name(), cfg, 0644)
	out, err := n.Test(tmpfile.Name()).CombinedOutput()
	if err != nil {
		return fmt.Errorf(`
-------------------------------------------------------------------------------
Error: %v
%v
-------------------------------------------------------------------------------
`, err, string(out))
	}

	os.Remove(tmpfile.Name())
	return nil
}

// OnUpdate is called by syncQueue in https://github.com/aledbf/ingress-controller/blob/master/pkg/ingress/controller/controller.go#L82
// periodically to keep the configuration in sync.
//
// convert configmap to custom configuration object (different in each implementation)
// write the custom template (the complexity depends on the implementation)
// write the configuration file
// returning nill implies the backend will be reloaded.
// if an error is returned means requeue the update
func (n NGINXController) OnUpdate(cmap *api.ConfigMap, ingressCfg ingress.Configuration) error {
	var longestName int
	var serverNames int
	for _, srv := range ingressCfg.Servers {
		serverNames += len([]byte(srv.Name))
		if longestName < len(srv.Name) {
			longestName = len(srv.Name)
		}
	}

	cfg := ngx_template.ReadConfig(cmap)

	// NGINX cannot resize the has tables used to store server names.
	// For this reason we check if the defined size defined is correct
	// for the FQDN defined in the ingress rules adjusting the value
	// if is required.
	// https://trac.nginx.org/nginx/ticket/352
	// https://trac.nginx.org/nginx/ticket/631
	nameHashBucketSize := nextPowerOf2(longestName)
	if nameHashBucketSize > cfg.ServerNameHashBucketSize {
		glog.V(3).Infof("adjusting ServerNameHashBucketSize variable from %v to %v",
			cfg.ServerNameHashBucketSize, nameHashBucketSize)
		cfg.ServerNameHashBucketSize = nameHashBucketSize
	}
	serverNameHashMaxSize := nextPowerOf2(serverNames)
	if serverNameHashMaxSize > cfg.ServerNameHashMaxSize {
		glog.V(3).Infof("adjusting ServerNameHashMaxSize variable from %v to %v",
			cfg.ServerNameHashMaxSize, serverNameHashMaxSize)
		cfg.ServerNameHashMaxSize = serverNameHashMaxSize
	}

	conf := make(map[string]interface{})
	// adjust the size of the backlog
	conf["backlogSize"] = sysctlSomaxconn()
	conf["upstreams"] = ingressCfg.Upstreams
	conf["servers"] = ingressCfg.Servers
	conf["tcpUpstreams"] = ingressCfg.TCPUpstreams
	conf["udpUpstreams"] = ingressCfg.UDPUpstreams
	conf["defResolver"] = cfg.Resolver
	conf["sslDHParam"] = ""
	conf["customErrors"] = len(cfg.CustomHTTPErrors) > 0
	conf["cfg"] = ngx_template.StandarizeKeyNames(cfg)

	data, err := n.t.Write(conf, n.testTemplate)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(cfgPath, data, 0644)
}

// http://graphics.stanford.edu/~seander/bithacks.html#RoundUpPowerOf2
// https://play.golang.org/p/TVSyCcdxUh
func nextPowerOf2(v int) int {
	v--
	v |= v >> 1
	v |= v >> 2
	v |= v >> 4
	v |= v >> 8
	v |= v >> 16
	v++

	return v
}
