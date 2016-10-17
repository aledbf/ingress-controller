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

import "github.com/prometheus/client_golang/prometheus"

const (
	// IngressSubsystem definition of Ingress sub system
	IngressSubsystem = "ingress_controller"
	// ReloadOperations ...
	ReloadOperations = "reload_operations"
	// ReloadOperationsError ...
	ReloadOperationsError = "reload_operations_errors"
)

func init() {
	prometheus.MustRegister(reloadOperations)
	prometheus.MustRegister(reloadOperationsErrors)
}

var (
	reloadOperations = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: "reload_operations",
			Name:      ReloadOperations,
			Help:      "Cumulative number of Ingress controller reload operations by operation type.",
		},
		[]string{"operation_type"},
	)
	reloadOperationsErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: IngressSubsystem,
			Name:      ReloadOperationsError,
			Help:      "Cumulative number of Ingress controller reload opetation errors by operation type.",
		},
		[]string{"operation_type"},
	)
)
