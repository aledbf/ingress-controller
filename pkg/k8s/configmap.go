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

package k8s

import (
	"bytes"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/golang/glog"

	"github.com/cbroglie/mapstructure"
	"github.com/fatih/structs"

	"github.com/aledbf/ingress-controller/pkg/ingress/defaults"

	"k8s.io/kubernetes/pkg/api"
)

const (
	customHTTPErrors  = "custom-http-errors"
	skipAccessLogUrls = "skip-access-log-urls"
)

var (
	camelRegexp = regexp.MustCompile("[0-9A-Za-z]+")
)

// StandarizeKeyNames ...
func StandarizeKeyNames(data interface{}) map[string]interface{} {
	return fixKeyNames(structs.Map(data))
}

// MergeConfigMapToStruct merges the content of a ConfigMap that contains
// mapstructure tags to a struct pointer using another pointer of the same
// type.
func MergeConfigMapToStruct(conf *api.ConfigMap, def, to interface{}) {
	//TODO: check def and to are the same type

	metadata := &mapstructure.Metadata{}
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		TagName:          "structs",
		Result:           &to,
		WeaklyTypedInput: true,
		Metadata:         metadata,
	})

	var errors []int
	if val, ok := conf.Data[customHTTPErrors]; ok {
		delete(conf.Data, customHTTPErrors)
		for _, i := range strings.Split(val, ",") {
			j, err := strconv.Atoi(i)
			if err != nil {
				glog.Warningf("%v is not a valid http code: %v", i, err)
			} else {
				errors = append(errors, j)
			}
		}
	}

	var skipUrls []string
	if val, ok := conf.Data[skipAccessLogUrls]; ok {
		delete(conf.Data, skipAccessLogUrls)
		skipUrls = strings.Split(val, ",")
	}

	err = decoder.Decode(conf.Data)
	if err != nil {
		glog.Infof("%v", err)
	}

	keyMap := getConfigKeyToStructKeyMap(to)
	valCM := reflect.Indirect(reflect.ValueOf(conf))
	for _, key := range metadata.Keys {
		fieldName, ok := keyMap[key]
		if !ok {
			continue
		}
		valDefault := reflect.ValueOf(def).Elem().FieldByName(fieldName)
		fieldCM := valCM.FieldByName(fieldName)
		if valDefault.IsValid() {
			valDefault.Set(fieldCM)
		}
	}

	b, ok := def.(defaults.Backend)
	if ok {
		b.CustomHTTPErrors = filterErrors(errors)
		b.SkipAccessLogURLs = skipUrls
		if b.Resolver == "" {
			b.Resolver = "" //TODO: ngx.defResolver
		}
	}

}

func filterErrors(errCodes []int) []int {
	var fa []int
	for _, errCode := range errCodes {
		if errCode > 299 && errCode < 600 {
			fa = append(fa, errCode)
		} else {
			glog.Warningf("error code %v is not valid for custom error pages", errCode)
		}
	}

	return fa
}

func fixKeyNames(data map[string]interface{}) map[string]interface{} {
	fixed := make(map[string]interface{})
	for k, v := range data {
		fixed[toCamelCase(k)] = v
	}

	return fixed
}

func toCamelCase(src string) string {
	byteSrc := []byte(src)
	chunks := camelRegexp.FindAll(byteSrc, -1)
	for idx, val := range chunks {
		if idx > 0 {
			chunks[idx] = bytes.Title(val)
		}
	}
	return string(bytes.Join(chunks, nil))
}

// getConfigKeyToStructKeyMap returns a map with the ConfigMapKey as key and the StructName as value.
func getConfigKeyToStructKeyMap(to interface{}) map[string]string {
	keyMap := map[string]string{}
	val := reflect.Indirect(reflect.ValueOf(to))
	for i := 0; i < val.Type().NumField(); i++ {
		fieldSt := val.Type().Field(i)
		configMapKey := strings.Split(fieldSt.Tag.Get("structs"), ",")[0]
		structKey := fieldSt.Name
		keyMap[configMapKey] = structKey
	}
	return keyMap
}
