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

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/apis/extensions"

	"github.com/aledbf/ingress-controller/pkg/ingress"
	ssl "github.com/aledbf/ingress-controller/pkg/net/ssl"
)

// syncSecret keeps in sync Secrets used by Ingress rules with files to allow
// being used in controllers.
func (ic *GenericController) syncSecret(k interface{}) error {
	ic.syncRateLimiter.Accept()
	if ic.secretQueue.IsShuttingDown() {
		return nil
	}
	if !ic.controllersInSync() {
		time.Sleep(podStoreSyncedPollPeriod)
		return fmt.Errorf("deferring sync till endpoints controller has synced")
	}

	// check if the default certificate is configured
	_, exists, err := ic.secretLister.Store.GetByKey(defServerName)
	if err != nil {
		return nil
	}
	var cert *ingress.SSLCert
	if !exists {
		if ic.cfg.DefaultSSLCertificate != "" {
			cert, err = ic.getPemCertificate(ic.cfg.DefaultSSLCertificate)
			if err != nil {
				return err
			}
		} else {
			defCert, defKey := ssl.GetFakeSSLCert()
			cert, err = ssl.AddOrUpdateCertAndKey("system-snake-oil-certificate", defCert, defKey, "")
			if err != nil {
				return nil
			}
		}
		err = ic.secretLister.Store.Add(&cert)
		if err != nil {
			return err
		}
	}

	key := k.(string)

	// get secret
	secObj, exists, err := ic.secrLister.Store.GetByKey(key)
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

	// create certificates and add or update the item in the store
	_, exists, err = ic.secretLister.Store.GetByKey(key)
	cert, err = ic.getPemCertificate(ic.cfg.DefaultSSLCertificate)
	if err != nil {
		return err
	}
	if exists {
		return ic.secretLister.Store.Update(cert)
	}
	return ic.secretLister.Store.Add(cert)
}

func (ic *GenericController) getPemCertificate(secretName string) (*ingress.SSLCert, error) {
	secretInterface, exists, err := ic.secrLister.Store.GetByKey(secretName)
	if err != nil {
		return nil, fmt.Errorf("Error retriveing secret %v: %v", secretName, err)
	}
	if !exists {
		return nil, fmt.Errorf("secret named %v does not exists", secretName)
	}

	secret := secretInterface.(*api.Secret)
	cert, ok := secret.Data[api.TLSCertKey]
	if !ok {
		return nil, fmt.Errorf("secret named %v has no private key", secretName)
	}
	key, ok := secret.Data[api.TLSPrivateKeyKey]
	if !ok {
		return nil, fmt.Errorf("secret named %v has no cert", secretName)
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
