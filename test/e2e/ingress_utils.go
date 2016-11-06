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
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"io/ioutil"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/kubernetes/pkg/api"

	apierrs "k8s.io/kubernetes/pkg/api/errors"
	"k8s.io/kubernetes/pkg/apis/extensions"
	clientset "k8s.io/kubernetes/pkg/client/clientset_generated/internalclientset"
	"k8s.io/kubernetes/pkg/labels"
	"k8s.io/kubernetes/pkg/runtime"
	utilnet "k8s.io/kubernetes/pkg/util/net"
	"k8s.io/kubernetes/pkg/util/sets"
	"k8s.io/kubernetes/pkg/util/wait"
	utilyaml "k8s.io/kubernetes/pkg/util/yaml"
	"k8s.io/kubernetes/test/e2e/framework"
	testutils "k8s.io/kubernetes/test/utils"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

const (
	rsaBits  = 2048
	validFor = 365 * 24 * time.Hour

	// Ingress class annotation defined in ingress repository.
	ingressClass = "kubernetes.io/ingress.class"
)

type testJig struct {
	client  clientset.Interface
	rootCAs map[string][]byte
	address string
	ing     *extensions.Ingress
	// class is the value of the annotation keyed under
	// `kubernetes.io/ingress.class`. It's added to all ingresses created by
	// this jig.
	class string
}

type conformanceTests struct {
	entryLog string
	execute  func()
	exitLog  string
}

func createComformanceTests(jig *testJig, ns string) []conformanceTests {
	manifestPath := filepath.Join(ingressManifestPath, "http")
	// These constants match the manifests used in ingressManifestPath
	tlsHost := "foo.bar.com"
	tlsSecretName := "foo"
	updatedTLSHost := "foobar.com"
	updateURLMapHost := "bar.baz.com"
	updateURLMapPath := "/testurl"
	// Platform agnostic list of tests that must be satisfied by all controllers
	return []conformanceTests{
		{
			fmt.Sprintf("should create a basic HTTP ingress"),
			func() { jig.createIngress(manifestPath, ns, map[string]string{}) },
			fmt.Sprintf("waiting for urls on basic HTTP ingress"),
		},
		{
			fmt.Sprintf("should terminate TLS for host %v", tlsHost),
			func() { jig.addHTTPS(tlsSecretName, tlsHost) },
			fmt.Sprintf("waiting for HTTPS updates to reflect in ingress"),
		},
		{
			fmt.Sprintf("should update SSL certificated with modified hostname %v", updatedTLSHost),
			func() {
				jig.update(func(ing *extensions.Ingress) {
					newRules := []extensions.IngressRule{}
					for _, rule := range ing.Spec.Rules {
						if rule.Host != tlsHost {
							newRules = append(newRules, rule)
							continue
						}
						newRules = append(newRules, extensions.IngressRule{
							Host:             updatedTLSHost,
							IngressRuleValue: rule.IngressRuleValue,
						})
					}
					ing.Spec.Rules = newRules
				})
				jig.addHTTPS(tlsSecretName, updatedTLSHost)
			},
			fmt.Sprintf("Waiting for updated certificates to accept requests for host %v", updatedTLSHost),
		},
		{
			fmt.Sprintf("should update url map for host %v to expose a single url: %v", updateURLMapHost, updateURLMapPath),
			func() {
				var pathToFail string
				jig.update(func(ing *extensions.Ingress) {
					newRules := []extensions.IngressRule{}
					for _, rule := range ing.Spec.Rules {
						if rule.Host != updateURLMapHost {
							newRules = append(newRules, rule)
							continue
						}
						existingPath := rule.IngressRuleValue.HTTP.Paths[0]
						pathToFail = existingPath.Path
						newRules = append(newRules, extensions.IngressRule{
							Host: updateURLMapHost,
							IngressRuleValue: extensions.IngressRuleValue{
								HTTP: &extensions.HTTPIngressRuleValue{
									Paths: []extensions.HTTPIngressPath{
										{
											Path:    updateURLMapPath,
											Backend: existingPath.Backend,
										},
									},
								},
							},
						})
					}
					ing.Spec.Rules = newRules
				})
				By("Checking that " + pathToFail + " is not exposed by polling for failure")
				route := fmt.Sprintf("http://%v%v", jig.address, pathToFail)
				ExpectNoError(pollURL(route, updateURLMapHost, lbCleanupTimeout, &http.Client{Timeout: reqTimeout}, true))
			},
			fmt.Sprintf("Waiting for path updates to reflect in L7"),
		},
	}
}

// pollURL polls till the url responds with a healthy http code. If
// expectUnreachable is true, it breaks on first non-healthy http code instead.
func pollURL(route, host string, timeout time.Duration, httpClient *http.Client, expectUnreachable bool) error {
	var lastBody string
	pollErr := wait.PollImmediate(lbPollInterval, timeout, func() (bool, error) {
		var err error
		lastBody, err = simpleGET(httpClient, route, host)
		if err != nil {
			framework.Logf("host %v path %v: %v unreachable", host, route, err)
			return expectUnreachable, nil
		}
		return !expectUnreachable, nil
	})
	if pollErr != nil {
		return fmt.Errorf("Failed to execute a successful GET within %v, Last response body for %v, host %v:\n%v\n\n%v\n",
			timeout, route, host, lastBody, pollErr)
	}
	return nil
}

// generateRSACerts generates a basic self signed certificate using a key length
// of rsaBits, valid for validFor time.
func generateRSACerts(host string, isCA bool, keyOut, certOut io.Writer) error {
	if len(host) == 0 {
		return fmt.Errorf("Require a non-empty host for client hello")
	}
	priv, err := rsa.GenerateKey(rand.Reader, rsaBits)
	if err != nil {
		return fmt.Errorf("Failed to generate key: %v", err)
	}
	notBefore := time.Now()
	notAfter := notBefore.Add(validFor)

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)

	if err != nil {
		return fmt.Errorf("failed to generate serial number: %s", err)
	}
	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   "default",
			Organization: []string{"Acme Co"},
		},
		NotBefore: notBefore,
		NotAfter:  notAfter,

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	hosts := strings.Split(host, ",")
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, h)
		}
	}

	if isCA {
		template.IsCA = true
		template.KeyUsage |= x509.KeyUsageCertSign
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return fmt.Errorf("Failed to create certificate: %s", err)
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}); err != nil {
		return fmt.Errorf("Failed creating cert: %v", err)
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)}); err != nil {
		return fmt.Errorf("Failed creating keay: %v", err)
	}
	return nil
}

// buildTransport creates a transport for use in executing HTTPS requests with
// the given certs. Note that the given rootCA must be configured with isCA=true.
func buildTransport(serverName string, rootCA []byte) (*http.Transport, error) {
	pool := x509.NewCertPool()
	ok := pool.AppendCertsFromPEM(rootCA)
	if !ok {
		return nil, fmt.Errorf("Unable to load serverCA.")
	}
	return utilnet.SetTransportDefaults(&http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: false,
			ServerName:         serverName,
			RootCAs:            pool,
		},
	}), nil
}

// buildInsecureClient returns an insecure http client. Can be used for "curl -k".
func buildInsecureClient(timeout time.Duration) *http.Client {
	t := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	return &http.Client{Timeout: timeout, Transport: utilnet.SetTransportDefaults(t)}
}

// createSecret creates a secret containing TLS certificates for the given Ingress.
// If a secret with the same name already exists in the namespace of the
// Ingress, it's updated.
func createSecret(kubeClient clientset.Interface, ing *extensions.Ingress) (host string, rootCA, privKey []byte, err error) {
	var k, c bytes.Buffer
	tls := ing.Spec.TLS[0]
	host = strings.Join(tls.Hosts, ",")
	framework.Logf("Generating RSA cert for host %v", host)

	if err = generateRSACerts(host, true, &k, &c); err != nil {
		return
	}
	cert := c.Bytes()
	key := k.Bytes()
	secret := &api.Secret{
		ObjectMeta: api.ObjectMeta{
			Name: tls.SecretName,
		},
		Data: map[string][]byte{
			api.TLSCertKey:       cert,
			api.TLSPrivateKeyKey: key,
		},
	}
	var s *api.Secret
	if s, err = kubeClient.Core().Secrets(ing.Namespace).Get(tls.SecretName); err == nil {
		// TODO: Retry the update. We don't really expect anything to conflict though.
		framework.Logf("Updating secret %v in ns %v with hosts %v for ingress %v", secret.Name, secret.Namespace, host, ing.Name)
		s.Data = secret.Data
		_, err = kubeClient.Core().Secrets(ing.Namespace).Update(s)
	} else {
		framework.Logf("Creating secret %v in ns %v with hosts %v for ingress %v", secret.Name, secret.Namespace, host, ing.Name)
		_, err = kubeClient.Core().Secrets(ing.Namespace).Create(secret)
	}
	return host, cert, key, err
}

func describeIng(ns string) {
	framework.Logf("\nOutput of kubectl describe ing:\n")
	desc, _ := framework.RunKubectl(
		"describe", "ing", fmt.Sprintf("--namespace=%v", ns))
	framework.Logf(desc)
}

// createIngress creates the Ingress and associated service/rc.
// Required: ing.yaml, rc.yaml, svc.yaml must exist in manifestPath
// Optional: secret.yaml, ingAnnotations
// If ingAnnotations is specified it will overwrite any annotations in ing.yaml
func (j *testJig) createIngress(manifestPath, ns string, ingAnnotations map[string]string) {
	mkpath := func(file string) string {
		return filepath.Join(framework.TestContext.RepoRoot, manifestPath, file)
	}

	framework.Logf("creating replication controller")
	framework.RunKubectlOrDie("create", "-f", mkpath("rc.yaml"), fmt.Sprintf("--namespace=%v", ns))

	framework.Logf("creating service")
	framework.RunKubectlOrDie("create", "-f", mkpath("svc.yaml"), fmt.Sprintf("--namespace=%v", ns))

	if exists(mkpath("secret.yaml")) {
		framework.Logf("creating secret")
		framework.RunKubectlOrDie("create", "-f", mkpath("secret.yaml"), fmt.Sprintf("--namespace=%v", ns))
	}
	j.ing = ingFromManifest(mkpath("ing.yaml"))
	j.ing.Namespace = ns
	j.ing.Annotations = map[string]string{ingressClass: j.class}
	for k, v := range ingAnnotations {
		j.ing.Annotations[k] = v
	}
	framework.Logf(fmt.Sprintf("creating" + j.ing.Name + " ingress"))
	var err error
	j.ing, err = j.client.Extensions().Ingresses(ns).Create(j.ing)
	ExpectNoError(err)
}

func (j *testJig) update(update func(ing *extensions.Ingress)) {
	var err error
	ns, name := j.ing.Namespace, j.ing.Name
	for i := 0; i < 3; i++ {
		j.ing, err = j.client.Extensions().Ingresses(ns).Get(name)
		if err != nil {
			framework.Failf("failed to get ingress %q: %v", name, err)
		}
		update(j.ing)
		j.ing, err = j.client.Extensions().Ingresses(ns).Update(j.ing)
		if err == nil {
			describeIng(j.ing.Namespace)
			return
		}
		if !apierrs.IsConflict(err) && !apierrs.IsServerTimeout(err) {
			framework.Failf("failed to update ingress %q: %v", name, err)
		}
	}
	framework.Failf("too many retries updating ingress %q", name)
}

func (j *testJig) addHTTPS(secretName string, hosts ...string) {
	j.ing.Spec.TLS = []extensions.IngressTLS{{Hosts: hosts, SecretName: secretName}}
	// TODO: Just create the secret in getRootCAs once we're watching secrets in
	// the ingress controller.
	_, cert, _, err := createSecret(j.client, j.ing)
	ExpectNoError(err)
	framework.Logf("Updating ingress %v to use secret %v for TLS termination", j.ing.Name, secretName)
	j.update(func(ing *extensions.Ingress) {
		ing.Spec.TLS = []extensions.IngressTLS{{Hosts: hosts, SecretName: secretName}}
	})
	j.rootCAs[secretName] = cert
}

func (j *testJig) getRootCA(secretName string) (rootCA []byte) {
	var ok bool
	rootCA, ok = j.rootCAs[secretName]
	if !ok {
		framework.Failf("Failed to retrieve rootCAs, no recorded secret by name %v", secretName)
	}
	return
}

func (j *testJig) deleteIngress() {
	ExpectNoError(j.client.Extensions().Ingresses(j.ing.Namespace).Delete(j.ing.Name, nil))
}

func (j *testJig) waitForIngress() {
	// Wait for the loadbalancer IP.
	address, err := framework.WaitForIngressAddress(j.client, j.ing.Namespace, j.ing.Name, lbPollTimeout)
	if err != nil {
		framework.Failf("Ingress failed to acquire an IP address within %v", lbPollTimeout)
	}
	j.address = address
	framework.Logf("Found address %v for ingress %v", j.address, j.ing.Name)
	timeoutClient := &http.Client{Timeout: reqTimeout}

	// Check that all rules respond to a simple GET.
	for _, rules := range j.ing.Spec.Rules {
		proto := "http"
		if len(j.ing.Spec.TLS) > 0 {
			knownHosts := sets.NewString(j.ing.Spec.TLS[0].Hosts...)
			if knownHosts.Has(rules.Host) {
				timeoutClient.Transport, err = buildTransport(rules.Host, j.getRootCA(j.ing.Spec.TLS[0].SecretName))
				ExpectNoError(err)
				proto = "https"
			}
		}
		for _, p := range rules.IngressRuleValue.HTTP.Paths {
			j.curlServiceNodePort(j.ing.Namespace, p.Backend.ServiceName, int(p.Backend.ServicePort.IntVal))
			route := fmt.Sprintf("%v://%v%v", proto, address, p.Path)
			framework.Logf("Testing route %v host %v with simple GET", route, rules.Host)
			ExpectNoError(pollURL(route, rules.Host, lbPollTimeout, timeoutClient, false))
		}
	}
}

// verifyURL polls for the given iterations, in intervals, and fails if the
// given url returns a non-healthy http code even once.
func (j *testJig) verifyURL(route, host string, iterations int, interval time.Duration, httpClient *http.Client) error {
	for i := 0; i < iterations; i++ {
		b, err := simpleGET(httpClient, route, host)
		if err != nil {
			framework.Logf(b)
			return err
		}
		framework.Logf("Verfied %v with host %v %d times, sleeping for %v", route, host, i, interval)
		time.Sleep(interval)
	}
	return nil
}

func (j *testJig) curlServiceNodePort(ns, name string, port int) {
	// TODO: Curl all nodes?
	u, err := framework.GetNodePortURL(j.client, ns, name, port)
	ExpectNoError(err)
	ExpectNoError(pollURL(u, "", 30*time.Second, &http.Client{Timeout: reqTimeout}, false))
}

// ingFromManifest reads a .json/yaml file and returns the rc in it.
func ingFromManifest(fileName string) *extensions.Ingress {
	var ing extensions.Ingress
	framework.Logf("Parsing ingress from %v", fileName)
	data, err := ioutil.ReadFile(fileName)
	ExpectNoError(err)

	json, err := utilyaml.ToJSON(data)
	ExpectNoError(err)

	ExpectNoError(runtime.DecodeInto(api.Codecs.UniversalDecoder(), json, &ing))
	return &ing
}

func exists(path string) bool {
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	framework.Failf("Failed to os.Stat path %v", path)
	return false
}

func newTestJig(c clientset.Interface) *testJig {
	return &testJig{client: c, rootCAs: map[string][]byte{}}
}

// NginxIngressController manages implementation details of Ingress on Nginx.
type NginxIngressController struct {
	ns         string
	rc         *api.ReplicationController
	pod        *api.Pod
	c          clientset.Interface
	externalIP string
}

func (cont *NginxIngressController) init() {
	mkpath := func(file string) string {
		return filepath.Join(framework.TestContext.RepoRoot, ingressManifestPath, "nginx", file)
	}
	framework.Logf("initializing nginx ingress controller")
	framework.RunKubectlOrDie("create", "-f", mkpath("rc.yaml"), fmt.Sprintf("--namespace=%v", cont.ns))

	rc, err := cont.c.Core().ReplicationControllers(cont.ns).Get("nginx-ingress-controller")
	ExpectNoError(err)
	cont.rc = rc

	framework.Logf("waiting for pods with label %v", rc.Spec.Selector)
	sel := labels.SelectorFromSet(labels.Set(rc.Spec.Selector))
	ExpectNoError(testutils.WaitForPodsWithLabelRunning(cont.c, cont.ns, sel))
	pods, err := cont.c.Core().Pods(cont.ns).List(api.ListOptions{LabelSelector: sel})
	ExpectNoError(err)
	if len(pods.Items) == 0 {
		framework.Failf("Failed to find nginx ingress controller pods with selector %v", sel)
	}
	cont.pod = &pods.Items[0]
	cont.externalIP, err = framework.GetHostExternalAddress(cont.c, cont.pod)
	ExpectNoError(err)
	framework.Logf("ingress controller running in pod %v on ip %v", cont.pod.Name, cont.externalIP)
}

func ExpectNoError(err error) {
	Expect(err).NotTo(HaveOccurred())
}

// simpleGET executes a get on the given url, returns error if non-200 returned.
func simpleGET(c *http.Client, url, host string) (string, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Host = host
	res, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	rawBody, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	body := string(rawBody)
	if res.StatusCode != http.StatusOK {
		err = fmt.Errorf(
			"GET returned http error %v", res.StatusCode)
	}
	return body, err
}
