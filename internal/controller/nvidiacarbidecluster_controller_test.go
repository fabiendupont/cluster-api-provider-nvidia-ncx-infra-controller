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

package controller

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrastructurev1 "github.com/fabiendupont/cluster-api-provider-nvidia-carbide/api/v1beta1"
	"github.com/fabiendupont/cluster-api-provider-nvidia-carbide/internal/controller/testutil"
	bmm "github.com/nvidia/bare-metal-manager-rest/sdk/standard"
)

var _ = Describe("NvidiaCarbideCluster Controller", func() {
	Context("When reconciling a NvidiaCarbideCluster", func() {
		const (
			clusterName      = "test-cluster"
			clusterNamespace = "default"
			orgName          = "test-org"
			siteID           = "550e8400-e29b-41d4-a716-446655440000"
			tenantID         = "660e8400-e29b-41d4-a716-446655440001"
		)

		var (
			ctx                  context.Context
			cluster              *clusterv1.Cluster
			nvidiaCarbideCluster *infrastructurev1.NvidiaCarbideCluster
		)

		BeforeEach(func() {
			ctx = context.Background()

			cluster = &clusterv1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: clusterNamespace,
				},
				Spec: clusterv1.ClusterSpec{
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{
						APIGroup: "infrastructure.cluster.x-k8s.io",
						Kind:     "NvidiaCarbideCluster",
						Name:     clusterName,
					},
				},
			}

			nvidiaCarbideCluster = &infrastructurev1.NvidiaCarbideCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: clusterNamespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "cluster.x-k8s.io/v1beta2",
							Kind:       "Cluster",
							Name:       clusterName,
							UID:        "test-uid",
						},
					},
				},
				Spec: infrastructurev1.NvidiaCarbideClusterSpec{
					SiteRef: infrastructurev1.SiteReference{
						ID: siteID,
					},
					TenantID: tenantID,
					VPC: infrastructurev1.VPCSpec{
						Name:                      "test-vpc",
						NetworkVirtualizationType: "ETHERNET_VIRTUALIZER",
					},
					Subnets: []infrastructurev1.SubnetSpec{
						{
							Name: "control-plane",
							CIDR: "10.0.1.0/24",
							Role: "control-plane",
						},
					},
					Authentication: infrastructurev1.AuthenticationSpec{
						SecretRef: corev1.SecretReference{
							Name:      "nvidia-carbide-creds",
							Namespace: clusterNamespace,
						},
					},
				},
			}
		})

		It("should successfully create VPC on first reconcile", func() {
			vpcID := uuid.New().String()
			mockClient := &testutil.MockCarbideClient{
				CreateVPCFunc: func(ctx context.Context, org string, req bmm.VpcCreateRequest) (*bmm.VPC, *http.Response, error) {
					Expect(org).To(Equal(orgName))
					Expect(req.Name).To(Equal("test-vpc"))
					Expect(req.SiteId).To(Equal(siteID))

					return &bmm.VPC{
						Id:     &vpcID,
						Name:   testutil.Ptr("test-vpc"),
						SiteId: testutil.Ptr(siteID),
					}, testutil.MockHTTPResponse(201), nil
				},
			}

			// Create credentials secret
			credsSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nvidia-carbide-creds",
					Namespace: clusterNamespace,
				},
				Data: map[string][]byte{
					"endpoint": []byte("https://api.carbide.test"),
					"orgName":  []byte(orgName),
					"token":    []byte("test-token"),
				},
			}

			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)
			_ = infrastructurev1.AddToScheme(scheme)
			_ = clusterv1.AddToScheme(scheme)

			k8sClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(cluster, nvidiaCarbideCluster, credsSecret).
				WithStatusSubresource(&infrastructurev1.NvidiaCarbideCluster{}).
				Build()

			reconciler := &NvidiaCarbideClusterReconciler{
				Client: k8sClient,
				Scheme: scheme,
			}

			// TODO: Implement actual reconcile with mock client injection
			// This requires updating the controller to accept a client factory
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      clusterName,
					Namespace: clusterNamespace,
				},
			}

			_, err := reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			// Verify VPC was created (would check status in real test)
			_ = mockClient // Use mock client to avoid unused variable
		})

		It("should handle VPC creation failure gracefully", func() {
			mockClient := &testutil.MockCarbideClient{
				CreateVPCFunc: func(ctx context.Context, org string, req bmm.VpcCreateRequest) (*bmm.VPC, *http.Response, error) {
					return nil, testutil.MockHTTPResponse(400), fmt.Errorf("invalid request")
				},
			}

			_ = mockClient // Placeholder for actual test implementation
			// TODO: Test error handling
		})
	})
})
