/*
Copyright 2016 The Kubernetes Authors.

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

package proxy

import (
	"testing"

	api "k8s.io/client-go/pkg/api/v1"
	extensions "k8s.io/client-go/pkg/apis/extensions/v1beta1"
	"k8s.io/client-go/pkg/util/intstr"

	"github.com/aledbf/ingress-controller/pkg/ingress/defaults"
)

func buildIngress() *extensions.Ingress {
	defaultBackend := extensions.IngressBackend{
		ServiceName: "default-backend",
		ServicePort: intstr.FromInt(80),
	}

	return &extensions.Ingress{
		ObjectMeta: api.ObjectMeta{
			Name:      "foo",
			Namespace: api.NamespaceDefault,
		},
		Spec: extensions.IngressSpec{
			Backend: &extensions.IngressBackend{
				ServiceName: "default-backend",
				ServicePort: intstr.FromInt(80),
			},
			Rules: []extensions.IngressRule{
				{
					Host: "foo.bar.com",
					IngressRuleValue: extensions.IngressRuleValue{
						HTTP: &extensions.HTTPIngressRuleValue{
							Paths: []extensions.HTTPIngressPath{
								{
									Path:    "/foo",
									Backend: defaultBackend,
								},
							},
						},
					},
				},
			},
		},
	}
}

func TestIngressHealthCheck(t *testing.T) {
	ing := buildIngress()

	data := map[string]string{}
	data[connect] = "1"
	data[send] = "2"
	data[read] = "3"
	data[bufferSize] = "1k"
	ing.SetAnnotations(data)

	cfg := defaults.Backend{UpstreamFailTimeout: 1}

	p := ParseAnnotations(cfg, ing)

	if p.ConnectTimeout != 1 {
		t.Errorf("Expected 1 as connect-timeout but returned %v", p.ConnectTimeout)
	}
	if p.SendTimeout != 2 {
		t.Errorf("Expected 2 as send-timeout but returned %v", p.SendTimeout)
	}
	if p.ReadTimeout != 3 {
		t.Errorf("Expected 3 as read-timeout but returned %v", p.ReadTimeout)
	}
	if p.BufferSize != "1k" {
		t.Errorf("Expected 1k as buffer-size but returned %v", p.BufferSize)
	}
}
