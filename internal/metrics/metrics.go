/*
Copyright 2026 Fabien Dupont.

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

package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	InstanceProvisioningDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "capi_ncx_infra_instance_provisioning_seconds",
			Help:    "Time from instance creation to Ready state",
			Buckets: []float64{30, 60, 120, 300, 600},
		},
		[]string{"site", "instance_type"},
	)
	VPCCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "capi_ncx_infra_vpcs_total",
			Help: "Number of managed VPCs",
		},
		[]string{"site"},
	)
	APIErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "capi_ncx_infra_api_errors_total",
			Help: "Carbide API errors by method and status code",
		},
		[]string{"method", "status_code"},
	)
	APILatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "capi_ncx_infra_api_latency_seconds",
			Help:    "Carbide API call latency by endpoint",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		},
		[]string{"method"},
	)
	MachinesManaged = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "capi_ncx_infra_machines_managed",
			Help: "Number of NcxInfraMachines currently managed",
		},
	)
	MachinesUnhealthy = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "capi_ncx_infra_machines_unhealthy",
			Help: "Number of NcxInfraMachines with active health faults",
		},
	)
)

func init() {
	metrics.Registry.MustRegister(
		InstanceProvisioningDuration,
		VPCCount,
		APIErrors,
		APILatency,
		MachinesManaged,
		MachinesUnhealthy,
	)
}
