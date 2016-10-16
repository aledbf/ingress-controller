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

package authtls

import (
	"fmt"

	"github.com/aledbf/ingress-controller/pkg/ingress/annotations/parser"
	"github.com/aledbf/ingress-controller/pkg/k8s"

	"k8s.io/kubernetes/pkg/apis/extensions"
)

const (
	// name of the secret
	authTLSSecret = "ingress.kubernetes.io/auth-tls-secret"
)

// TLS returns external authentication configuration for an Ingress rule
type TLS struct {
	Namespace string
	Name      string
}

// ParseAnnotations parses the annotations contained in the ingress
// rule used to use an external URL as source for authentication
func ParseAnnotations(ing *extensions.Ingress) (TLS, error) {
	if ing.GetAnnotations() == nil {
		return TLS{}, parser.ErrMissingAnnotations
	}

	str, err := parser.GetStringAnnotation(authTLSSecret, ing)
	if err != nil {
		return TLS{}, err
	}

	if str == "" {
		return TLS{}, fmt.Errorf("an empty string is not a valid secret name")
	}

	ns, name, err := k8s.ParseNameNS(str)
	if err != nil {
		return TLS{}, err
	}

	return TLS{
		Name:      "",
		Namespace: "",
	}, nil
}
