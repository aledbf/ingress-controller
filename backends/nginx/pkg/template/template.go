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

package template

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	text_template "text/template"

	"github.com/golang/glog"

	"github.com/aledbf/ingress-controller/pkg/ingress"
	"github.com/aledbf/ingress-controller/pkg/watch"
)

const (
	slash = "/"
)

var (
	camelRegexp = regexp.MustCompile("[0-9A-Za-z]+")
)

// Template ...
type Template struct {
	tmpl *text_template.Template
	fw   watch.FileWatcher
}

//NewTemplate returns a new Template instance or an
//error if the specified template file contains errors
func NewTemplate(file string, onChange func()) (*Template, error) {
	tmpl, err := text_template.New("nginx.tmpl").Funcs(funcMap).ParseFiles(file)
	if err != nil {
		return nil, err
	}
	fw, err := watch.NewFileWatcher(file, onChange)
	if err != nil {
		return nil, err
	}

	return &Template{
		tmpl: tmpl,
		fw:   fw,
	}, nil
}

// Close removes the file watcher
func (t *Template) Close() {
	t.fw.Close()
}

// Write populates a buffer using a template with NGINX configuration
// and the servers and upstreams created by Ingress rules
func (t *Template) Write(conf map[string]interface{},
	isValidTemplate func([]byte) error) ([]byte, error) {

	if glog.V(3) {
		b, err := json.Marshal(conf)
		if err != nil {
			glog.Errorf("unexpected error: %v", err)
		}
		glog.Infof("NGINX configuration: %v", string(b))
	}

	buffer := new(bytes.Buffer)
	err := t.tmpl.Execute(buffer, conf)

	// cat -s squeezes multiple adjacent empty lines to be single
	// spaced this is to avoid the use of regular expressions
	cmd := exec.Command("cat", "-s")
	cmd.Stdin = buffer
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		glog.Warningf("unexpected error cleaning template: %v", err)
		return buffer.Bytes(), nil
	}

	content := out.Bytes()
	err = isValidTemplate(content)
	if err != nil {
		return nil, err
	}

	return content, nil
}

var (
	funcMap = text_template.FuncMap{
		"empty": func(input interface{}) bool {
			check, ok := input.(string)
			if ok {
				return len(check) == 0
			}
			return true
		},
		"buildLocation":            buildLocation,
		"buildAuthLocation":        buildAuthLocation,
		"buildProxyPass":           buildProxyPass,
		"buildRateLimitZones":      buildRateLimitZones,
		"buildRateLimit":           buildRateLimit,
		"getSSPassthroughUpstream": getSSPassthroughUpstream,

		"contains":  strings.Contains,
		"hasPrefix": strings.HasPrefix,
		"hasSuffix": strings.HasSuffix,
		"toUpper":   strings.ToUpper,
		"toLower":   strings.ToLower,
	}
)

func getSSPassthroughUpstream(input interface{}) string {
	s, ok := input.(*ingress.Server)
	if !ok {
		return ""
	}

	return s.Name
}

// buildLocation produces the location string, if the ingress has redirects
// (specified through the ingress.kubernetes.io/rewrite-to annotation)
func buildLocation(input interface{}) string {
	location, ok := input.(*ingress.Location)
	if !ok {
		return slash
	}

	path := location.Path
	if len(location.Redirect.Target) > 0 && location.Redirect.Target != path {
		return fmt.Sprintf("~* %s", path)
	}

	return path
}

func buildAuthLocation(input interface{}) string {
	location, ok := input.(*ingress.Location)
	if !ok {
		return ""
	}

	if location.ExternalAuth.URL == "" {
		return ""
	}

	str := base64.URLEncoding.EncodeToString([]byte(location.Path))
	// avoid locations containing the = char
	str = strings.Replace(str, "=", "", -1)
	return fmt.Sprintf("/_external-auth-%v", str)
}

// buildProxyPass produces the proxy pass string, if the ingress has redirects
// (specified through the ingress.kubernetes.io/rewrite-to annotation)
// If the annotation ingress.kubernetes.io/add-base-url:"true" is specified it will
// add a base tag in the head of the response from the service
func buildProxyPass(input interface{}) string {
	location, ok := input.(*ingress.Location)
	if !ok {
		return ""
	}

	path := location.Path

	proto := "http"
	if location.SecureUpstream {
		proto = "https"
	}
	// defProxyPass returns the default proxy_pass, just the name of the upstream
	defProxyPass := fmt.Sprintf("proxy_pass %s://%s;", proto, location.Upstream.Name)
	// if the path in the ingress rule is equals to the target: no special rewrite
	if path == location.Redirect.Target {
		return defProxyPass
	}

	if path != slash && !strings.HasSuffix(path, slash) {
		path = fmt.Sprintf("%s/", path)
	}

	if len(location.Redirect.Target) > 0 {
		abu := ""
		if location.Redirect.AddBaseURL {
			bPath := location.Redirect.Target
			if !strings.HasSuffix(bPath, slash) {
				bPath = fmt.Sprintf("%s/", bPath)
			}

			abu = fmt.Sprintf(`subs_filter '<head(.*)>' '<head$1><base href="$scheme://$server_name%v">' r;
	subs_filter '<HEAD(.*)>' '<HEAD$1><base href="$scheme://$server_name%v">' r;
	`, bPath, bPath)
		}

		if location.Redirect.Target == slash {
			// special case redirect to /
			// ie /something to /
			return fmt.Sprintf(`
	rewrite %s(.*) /$1 break;
	rewrite %s / break;
	proxy_pass %s://%s;
	%v`, path, location.Path, proto, location.Upstream.Name, abu)
		}

		return fmt.Sprintf(`
	rewrite %s(.*) %s/$1 break;
	proxy_pass %s://%s;
	%v`, path, location.Redirect.Target, proto, location.Upstream.Name, abu)
	}

	// default proxy_pass
	return defProxyPass
}

// buildRateLimitZones produces an array of limit_conn_zone in order to allow
// rate limiting of request. Each Ingress rule could have up to two zones, one
// for connection limit by IP address and other for limiting request per second
func buildRateLimitZones(input interface{}) []string {
	zones := []string{}

	servers, ok := input.([]*ingress.Server)
	if !ok {
		return zones
	}

	for _, server := range servers {
		for _, loc := range server.Locations {

			if loc.RateLimit.Connections.Limit > 0 {
				zone := fmt.Sprintf("limit_conn_zone $binary_remote_addr zone=%v:%vm;",
					loc.RateLimit.Connections.Name,
					loc.RateLimit.Connections.SharedSize)
				zones = append(zones, zone)
			}

			if loc.RateLimit.RPS.Limit > 0 {
				zone := fmt.Sprintf("limit_conn_zone $binary_remote_addr zone=%v:%vm rate=%vr/s;",
					loc.RateLimit.Connections.Name,
					loc.RateLimit.Connections.SharedSize,
					loc.RateLimit.Connections.Limit)
				zones = append(zones, zone)
			}
		}
	}

	return zones
}

// buildRateLimit produces an array of limit_req to be used inside the Path of
// Ingress rules. The order: connections by IP first and RPS next.
func buildRateLimit(input interface{}) []string {
	limits := []string{}

	loc, ok := input.(*ingress.Location)
	if !ok {
		return limits
	}

	if loc.RateLimit.Connections.Limit > 0 {
		limit := fmt.Sprintf("limit_conn %v %v;",
			loc.RateLimit.Connections.Name, loc.RateLimit.Connections.Limit)
		limits = append(limits, limit)
	}

	if loc.RateLimit.RPS.Limit > 0 {
		limit := fmt.Sprintf("limit_req zone=%v burst=%v nodelay;",
			loc.RateLimit.Connections.Name, loc.RateLimit.Connections.Burst)
		limits = append(limits, limit)
	}

	return limits
}
