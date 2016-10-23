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

package nginx

import (
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"log"
	"os"

	"github.com/golang/glog"

	"github.com/aledbf/ingress-controller/pkg/ingress"
)

// AddOrUpdateCertAndKey creates a .pem file wth the cert and the key with the specified name
func AddOrUpdateCertAndKey(name string, cert string, key string, ca string) (*ingress.SSLCert, error) {
	pemName := fmt.Sprintf("%v.pem", name)
	pemFileName := fmt.Sprintf("%v/%v", ingress.DefaultSSLDirectory, pemName)

	tempPemFile, err := ioutil.TempFile("", pemName)
	if err != nil {
		return nil, fmt.Errorf("Couldn't create temp pem file %v: %v", tempPemFile.Name(), err)
	}

	_, err = tempPemFile.WriteString(fmt.Sprintf("%v\n%v", cert, key))
	if err != nil {
		return nil, fmt.Errorf("Couldn't write to pem file %v: %v", tempPemFile.Name(), err)
	}

	err = tempPemFile.Close()
	if err != nil {
		return nil, fmt.Errorf("Couldn't close temp pem file %v: %v", tempPemFile.Name(), err)
	}

	pemCerts, err := ioutil.ReadFile(tempPemFile.Name())
	if err != nil {
		return nil, err
	}

	pembBock, _ := pem.Decode(pemCerts)
	if pembBock == nil {
		return nil, fmt.Errorf("No valid PEM formatted block found")
	}

	pemCert, err := x509.ParseCertificate(pembBock.Bytes)
	if err != nil {
		return nil, err
	}

	cn := []string{pemCert.Subject.CommonName}
	if len(pemCert.DNSNames) > 0 {
		cn = append(cn, pemCert.DNSNames...)
	}

	if ca != "" {
		cck, err := tls.LoadX509KeyPair(cert, key)
		if err != nil {
			log.Fatalf("client: loadkeys: %s", err)
		}
		if len(cck.Certificate) != 2 {
			return nil, fmt.Errorf("should have 2 concatenated certificates: cert and key")
		}

		caCert, err := ioutil.ReadFile(ca)
		if err != nil {
			return nil, err
		}

		caCertPool := x509.NewCertPool()
		caCertPool.AppendCertsFromPEM(caCert)
		// Create tls config
		tlsConfig := &tls.Config{
			Certificates: []tls.Certificate{cck},
			RootCAs:      caCertPool,
		}
		tlsConfig.BuildNameToCertificate()
	}

	if err != nil {
		os.Remove(tempPemFile.Name())
		return nil, err
	}

	err = os.Rename(tempPemFile.Name(), pemFileName)
	if err != nil {
		os.Remove(tempPemFile.Name())
		return nil, fmt.Errorf("Couldn't move temp pem file %v to destination %v: %v", tempPemFile.Name(), pemFileName, err)
	}

	return &ingress.SSLCert{
		CertFileName: cert,
		KeyFileName:  key,
		PemFileName:  pemFileName,
		PemSHA:       pemSHA1(pemFileName),
		CN:           cn,
	}, nil
}

// SearchDHParamFile iterates all the secrets mounted inside the /etc/nginx-ssl directory
// in order to find a file with the name dhparam.pem. If such file exists it will
// returns the path. If not it just returns an empty string
func SearchDHParamFile(baseDir string) string {
	files, _ := ioutil.ReadDir(baseDir)
	for _, file := range files {
		if !file.IsDir() {
			continue
		}

		dhPath := fmt.Sprintf("%v/%v/dhparam.pem", baseDir, file.Name())
		if _, err := os.Stat(dhPath); err == nil {
			glog.Infof("using file '%v' for parameter ssl_dhparam", dhPath)
			return dhPath
		}
	}

	glog.Warning("no file dhparam.pem found in secrets")
	return ""
}

// pemSHA1 returns the SHA1 of a pem file. This is used to
// reload NGINX in case a secret with a SSL certificate changed.
func pemSHA1(filename string) string {
	hasher := sha1.New()
	s, err := ioutil.ReadFile(filename)
	if err != nil {
		return ""
	}

	hasher.Write(s)
	return hex.EncodeToString(hasher.Sum(nil))
}

const (
	snakeOilPem = "/etc/ssl/certs/ssl-cert-snakeoil.pem"
	snakeOilKey = "/etc/ssl/private/ssl-cert-snakeoil.key"
)

// GetFakeSSLCert returns the snake oil ssl certificate created by the command
// make-ssl-cert generate-default-snakeoil --force-overwrite
func GetFakeSSLCert() (string, string) {
	cert, err := ioutil.ReadFile(snakeOilPem)
	if err != nil {
		return "", ""
	}

	key, err := ioutil.ReadFile(snakeOilKey)
	if err != nil {
		return "", ""
	}

	return string(cert), string(key)
}
