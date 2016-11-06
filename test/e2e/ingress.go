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

package e2e

import (
	"k8s.io/kubernetes/test/e2e/framework"

	. "github.com/onsi/ginkgo"
)

const (
	// parent path to yaml test manifests.
	ingressManifestPath = "test/e2e/testing-manifests/ingress"
	dsTemplate          = `
apiVersion: v1
kind: Service
metadata:
  name: default-http-backend
  labels:
    k8s-app: default-http-backend
spec:
  ports:
  - port: 80
    targetPort: 8080
    protocol: TCP
    name: http
  selector:
    k8s-app: default-http-backend
---
apiVersion: v1
kind: ReplicationController
metadata:
  name: default-http-backend
spec:
  replicas: 1
  selector:
    k8s-app: default-http-backend
  template:
    metadata:
      labels:
        k8s-app: default-http-backend
    spec:
      terminationGracePeriodSeconds: 60
      containers:
      - name: default-http-backend
        # Any image is permissable as long as:
        # 1. It serves a 404 page at /
        # 2. It serves 200 on a /healthz endpoint
        image: gcr.io/google_containers/defaultbackend:1.0
        livenessProbe:
          httpGet:
            path: /healthz
            port: 8080
            scheme: HTTP
          initialDelaySeconds: 30
          timeoutSeconds: 5
        ports:
        - containerPort: 8080
        resources:
          limits:
            cpu: 10m
            memory: 20Mi
          requests:
            cpu: 10m
            memory: 20Mi
---	
apiVersion: extensions/v1beta1
kind: DaemonSet
metadata:
  name: nginx-ingress-controller
spec:
  template:
    metadata:
      labels:
        name: nginx-ingress-controller
    spec:
      containers:
      - image: {{ .image }}
        name: nginx-ingress-controller
        readinessProbe:
          httpGet:
            path: /healthz
            port: 10254
            scheme: HTTP
        livenessProbe:
          httpGet:
            path: /healthz
            port: 18080
            scheme: HTTP
          initialDelaySeconds: 10
          timeoutSeconds: 1
        # use downward API
        env:
          - name: POD_NAME
            valueFrom:
              fieldRef:
                fieldPath: metadata.name
          - name: POD_NAMESPACE
            valueFrom:
              fieldRef:
                fieldPath: metadata.namespace
        ports:
        - containerPort: 80
          hostPort: 80
        - containerPort: 443
          hostPort: 443
        - containerPort: 18080
          hostPort: 18080		  
        args:
        - /nginx-ingress-controller
        - --default-backend-service=default/default-http-backend
`
)

var _ = framework.KubeDescribe("Ingress controllers: [Feature:Ingress]", func() {
	defer GinkgoRecover()
	var (
		ns               string
		jig              *testJig
		conformanceTests []conformanceTests
	)
	f := framework.NewDefaultFramework("ingress")

	BeforeEach(func() {
		f.BeforeEach()
		jig = newTestJig(f.ClientSet)
		ns = f.Namespace.Name
	})

	// Time: borderline 5m, slow by design
	framework.KubeDescribe("NGINX [Slow]", func() {
		var nginxController *NginxIngressController

		BeforeEach(func() {
			By("Initializing nginx controller")
			jig.class = "nginx"
			nginxController = &NginxIngressController{ns: ns, c: jig.client}
			nginxController.init()
		})

		AfterEach(func() {
			if CurrentGinkgoTestDescription().Failed {
				describeIng(ns)
			}
			if jig.ing == nil {
				By("No ingress created, no cleanup necessary")
				return
			}
			By("Deleting ingress")
			jig.deleteIngress()
			By("Deleting ingress controller")
			framework.RunKubectlOrDie("delete", "deployments", "nginx-ingress-controller", "--namespace", ns)
		})

		It("should conform to Ingress spec", func() {
			conformanceTests = createComformanceTests(jig, ns)
			for _, t := range conformanceTests {
				By(t.entryLog)
				t.execute()
				By(t.exitLog)
				jig.waitForIngress()
			}
		})
	})
})
