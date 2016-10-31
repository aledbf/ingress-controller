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
	"k8s.io/kubernetes/pkg/client/cache"

	"github.com/aledbf/ingress-controller/pkg/ingress"
	"github.com/aledbf/ingress-controller/pkg/ingress/annotations/parser"
	ssl "github.com/aledbf/ingress-controller/pkg/net/ssl"
	"github.com/golang/glog"
)

// syncSecret keeps in sync Secrets used by Ingress rules with files to allow
// being used in controllers.
func (ic *GenericController) syncSecret(k interface{}) error {
	if ic.secretQueue.IsShuttingDown() {
		return nil
	}
	if !ic.controllersInSync() {
		time.Sleep(podStoreSyncedPollPeriod)
		return fmt.Errorf("deferring sync till endpoints controller has synced")
	}

	// check if the default certificate is configured
	key := fmt.Sprintf("default/%v", defServerName)
	_, exists := ic.sslCertTracker.Get(key)
	var cert *ingress.SSLCert
	var err error
	if !exists {
		if ic.cfg.DefaultSSLCertificate != "" {
			cert, err = ic.getPemCertificate(ic.cfg.DefaultSSLCertificate)
			if err != nil {
				return err
			}
		} else {
			defCert, defKey := ssl.GetFakeSSLCert()
			cert, err = ssl.AddOrUpdateCertAndKey("system-snake-oil-certificate", defCert, defKey, []byte{})
			if err != nil {
				return nil
			}
		}
		cert.Name = defServerName
		cert.Namespace = api.NamespaceDefault
		ic.sslCertTracker.Add(key, cert)
	}

	key = k.(string)

	// get secret
	secObj, exists, err := ic.secrLister.Store.GetByKey(key)
	if err != nil {
		return fmt.Errorf("error getting secret %v: %v", key, err)
	}
	if !exists {
		return fmt.Errorf("secret %v was not found", key)
	}
	sec := secObj.(*api.Secret)
	if !ic.secrReferenced(sec.Name, sec.Namespace) {
		glog.V(2).Infof("secret %v/%v is not used in Ingress rules. skipping ", sec.Namespace, sec.Name)
		return nil
	}

	cert, err = ic.getPemCertificate(key)
	if err != nil {
		return err
	}

	// create certificates and add or update the item in the store
	_, exists = ic.sslCertTracker.Get(key)
	if exists {
		ic.sslCertTracker.Update(key, cert)
		return nil
	}
	ic.sslCertTracker.Add(key, cert)
	return nil
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
	s, err := ssl.AddOrUpdateCertAndKey(nsSecName, cert, key, ca)
	if err != nil {
		return nil, err
	}

	s.Name = secret.Name
	s.Namespace = secret.Namespace
	return s, nil
}

// check if secret is referenced in this controller's config
func (ic *GenericController) secrReferenced(name, namespace string) bool {
	for _, ingIf := range ic.ingLister.Store.List() {
		ing := ingIf.(*extensions.Ingress)
		str, err := parser.GetStringAnnotation("ingress.kubernetes.io/auth-tls-secret", ing)
		if err == nil && str == fmt.Sprintf("%v/%v", namespace, name) {
			return true
		}

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

// sslCertTracker ...
type sslCertTracker struct {
	cache.ThreadSafeStore
}

func newSSLCertTracker() *sslCertTracker {
	return &sslCertTracker{
		cache.NewThreadSafeStore(cache.Indexers{}, cache.Indices{}),
	}
}
