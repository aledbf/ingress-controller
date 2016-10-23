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
	"strings"
	"time"

	"github.com/golang/glog"

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/apis/extensions"

	"github.com/aledbf/ingress-controller/pkg/ingress"
	ssl "github.com/aledbf/ingress-controller/pkg/net/ssl"
)

// syncSecret keeps in sync Secrets used by Ingress rules with files to allow
// being used in controllers.
func (ic *GenericController) syncSecret(key interface{}) error {
	ic.syncRateLimiter.Accept()

	if ic.secretQueue.IsShuttingDown() {
		return nil
	}
	if !ic.controllersInSync() {
		time.Sleep(podStoreSyncedPollPeriod)
		return fmt.Errorf("deferring sync till endpoints controller has synced")
	}

	secObj, exists, err := ic.secrLister.Store.GetByKey(key.(string))
	if err != nil {
		return fmt.Errorf("error getting secret %v: %v", key, err)
	}
	if !exists {
		return fmt.Errorf("service %v was not found", key)
	}
	sec := secObj.(*api.Secret)
	if !ic.secrReferenced(sec.Name, sec.Namespace) {
		return nil
	}

	return nil
}

func (ic *GenericController) getPemsFromIngress(data []interface{}) map[string]ingress.SSLCert {
	pems := make(map[string]ingress.SSLCert)

	for _, ingIf := range data {
		ing := ingIf.(*extensions.Ingress)
		for _, tls := range ing.Spec.TLS {
			secretName := tls.SecretName
			if secretName == "" {
				glog.Errorf("No secretName defined for hosts")
				continue
			}

			secretKey := fmt.Sprintf("%s/%s", ing.Namespace, secretName)
			ngxCert, err := ic.getPemCertificate(secretKey)
			if err != nil {
				glog.Warningf("%v", err)
				continue
			}

			for _, host := range tls.Hosts {
				if isHostValid(host, ngxCert.CN) {
					pems[host] = ngxCert
				} else {
					glog.Warningf("SSL Certificate stored in secret %v is not valid for the host %v defined in the Ingress rule %v", secretName, host, ing.Name)
				}
			}
		}
	}

	return pems
}

func (ic *GenericController) getPemCertificate(secretName string) (ingress.SSLCert, error) {
	secretInterface, exists, err := ic.secrLister.Store.GetByKey(secretName)
	if err != nil {
		return ingress.SSLCert{}, fmt.Errorf("Error retriveing secret %v: %v", secretName, err)
	}
	if !exists {
		return ingress.SSLCert{}, fmt.Errorf("secret named %v does not exists", secretName)
	}

	secret := secretInterface.(*api.Secret)
	cert, ok := secret.Data[api.TLSCertKey]
	if !ok {
		return ingress.SSLCert{}, fmt.Errorf("secret named %v has no private key", secretName)
	}
	key, ok := secret.Data[api.TLSPrivateKeyKey]
	if !ok {
		return ingress.SSLCert{}, fmt.Errorf("secret named %v has no cert", secretName)
	}

	ca := secret.Data["ca.crt"]

	nsSecName := strings.Replace(secretName, "/", "-", -1)
	return ssl.AddOrUpdateCertAndKey(nsSecName, string(cert), string(key), string(ca))
}

// check if secret is referenced in this controller's config
func (ic *GenericController) secrReferenced(namespace string, name string) bool {
	for _, ingIf := range ic.ingLister.Store.List() {
		ing := ingIf.(*extensions.Ingress)
		if ing.Namespace != namespace {
			continue
		}
		for _, tls := range ing.Spec.TLS {
			if tls.SecretName == name {
				return true
			}
		}
	}
	return false
}
