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
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrastructurev1 "github.com/fabiendupont/cluster-api-provider-nvidia-ncx-infra-controller/api/v1beta1"
	"github.com/fabiendupont/cluster-api-provider-nvidia-ncx-infra-controller/internal/controller/testutil"
	"github.com/fabiendupont/cluster-api-provider-nvidia-ncx-infra-controller/pkg/scope"
	nico "github.com/NVIDIA/ncx-infra-controller-rest/sdk/standard"
)

var _ = Describe("NcxInfraMachine Controller", func() {
	const (
		clusterName      = "test-cluster"
		machineName      = "test-machine-0"
		clusterNamespace = "default"
		orgName          = "test-org"
		siteID           = "550e8400-e29b-41d4-a716-446655440000"
		tenantID         = "660e8400-e29b-41d4-a716-446655440001"
	)

	var (
		ctx                  context.Context
		cluster              *clusterv1.Cluster
		machine              *clusterv1.Machine
		nvidiaCarbideCluster *infrastructurev1.NcxInfraCluster
		nvidiaCarbideMachine *infrastructurev1.NcxInfraMachine
		credsSecret          *corev1.Secret
		bootstrapSecret      *corev1.Secret
		namespacedName       types.NamespacedName
	)

	BeforeEach(func() {
		ctx = context.Background()
		namespacedName = types.NamespacedName{
			Name:      machineName,
			Namespace: clusterNamespace,
		}

		bootstrapSecretName := "bootstrap-data"
		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterName,
				Namespace: clusterNamespace,
				UID:       "cluster-uid",
			},
			Spec: clusterv1.ClusterSpec{
				InfrastructureRef: clusterv1.ContractVersionedObjectReference{
					APIGroup: "infrastructure.cluster.x-k8s.io",
					Kind:     "NcxInfraCluster",
					Name:     clusterName,
				},
			},
		}

		machine = &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      machineName,
				Namespace: clusterNamespace,
				UID:       "machine-uid",
				Labels: map[string]string{
					clusterv1.ClusterNameLabel: clusterName,
				},
			},
			Spec: clusterv1.MachineSpec{
				ClusterName: clusterName,
				Bootstrap: clusterv1.Bootstrap{
					DataSecretName: &bootstrapSecretName,
				},
			},
		}

		subnetID := uuid.New().String()
		vpcID := uuid.New().String()

		nvidiaCarbideCluster = &infrastructurev1.NcxInfraCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterName,
				Namespace: clusterNamespace,
			},
			Spec: infrastructurev1.NcxInfraClusterSpec{
				SiteRef:  infrastructurev1.SiteReference{ID: siteID},
				TenantID: tenantID,
				VPC: infrastructurev1.VPCSpec{
					Name:                      "test-vpc",
					NetworkVirtualizationType: "ETHERNET_VIRTUALIZER",
				},
				Subnets: []infrastructurev1.SubnetSpec{
					{Name: "control-plane", CIDR: "10.0.1.0/24", Role: "control-plane"},
				},
				Authentication: infrastructurev1.AuthenticationSpec{
					SecretRef: corev1.SecretReference{
						Name:      "ncx-infra-creds",
						Namespace: clusterNamespace,
					},
				},
			},
			Status: infrastructurev1.NcxInfraClusterStatus{
				Ready: true,
				VPCID: vpcID,
				NetworkStatus: infrastructurev1.NetworkStatus{
					SubnetIDs: map[string]string{"control-plane": subnetID},
				},
			},
		}

		nvidiaCarbideMachine = &infrastructurev1.NcxInfraMachine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      machineName,
				Namespace: clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "cluster.x-k8s.io/v1beta2",
						Kind:       "Machine",
						Name:       machineName,
						UID:        "machine-uid",
					},
				},
			},
			Spec: infrastructurev1.NcxInfraMachineSpec{
				InstanceType: infrastructurev1.InstanceTypeSpec{
					ID: "instance-type-uuid",
				},
				Network: infrastructurev1.NetworkSpec{
					SubnetName: "control-plane",
				},
			},
		}

		credsSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "ncx-infra-creds",
				Namespace: clusterNamespace,
			},
			Data: map[string][]byte{
				"endpoint": []byte("https://api.ncx-infra.test"),
				"orgName":  []byte(orgName),
				"token":    []byte("test-token"),
			},
		}

		bootstrapSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      bootstrapSecretName,
				Namespace: clusterNamespace,
			},
			Data: map[string][]byte{
				"value": []byte("#cloud-config\nruncmd:\n  - echo hello"),
			},
		}
	})

	Context("When reconciling instance creation", func() {
		It("should create instance and set providerID in status", func() {
			instanceID := uuid.New().String()
			physMachineID := uuid.New().String()
			status := nico.InstanceStatus("Provisioning")

			mockClient := &testutil.MockNcxInfraClient{
				CreateInstanceFunc: func(ctx context.Context, org string, req nico.InstanceCreateRequest) (*nico.Instance, *http.Response, error) {
					Expect(org).To(Equal(orgName))
					Expect(req.Name).To(Equal(machineName))
					Expect(req.VpcId).To(Equal(nvidiaCarbideCluster.Status.VPCID))
					Expect(*req.PhoneHomeEnabled).To(BeTrue())
					return &nico.Instance{
						Id:        &instanceID,
						Name:      testutil.Ptr(machineName),
						MachineId: *nico.NewNullableString(&physMachineID),
						Status:    &status,
					}, testutil.MockHTTPResponse(201), nil
				},
				GetAllInstanceFunc: func(ctx context.Context, org string) ([]nico.Instance, *http.Response, error) {
					return []nico.Instance{}, testutil.MockHTTPResponse(200), nil
				},
			}

			nvidiaCarbideMachine.Finalizers = []string{NcxInfraMachineFinalizer}

			scheme := newTestScheme()
			k8sClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(cluster, machine, nvidiaCarbideCluster, nvidiaCarbideMachine, credsSecret, bootstrapSecret).
				WithStatusSubresource(
					&infrastructurev1.NcxInfraMachine{},
					&infrastructurev1.NcxInfraCluster{},
					&clusterv1.Machine{},
				).
				Build()

			reconciler := &NcxInfraMachineReconciler{
				Client:              k8sClient,
				Scheme:              scheme,
				NcxInfraClient: mockClient,
				OrgName:             orgName,
			}

			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())
			// Should requeue to check instance status
			Expect(result.RequeueAfter).NotTo(BeZero())

			// Verify status was updated
			updatedMachine := &infrastructurev1.NcxInfraMachine{}
			Expect(k8sClient.Get(ctx, namespacedName, updatedMachine)).To(Succeed())
			Expect(updatedMachine.Status.InstanceID).To(Equal(instanceID))
			Expect(updatedMachine.Status.MachineID).To(Equal(physMachineID))
			Expect(updatedMachine.Status.ProviderID).NotTo(BeNil())
			Expect(*updatedMachine.Status.ProviderID).To(ContainSubstring("nico://"))
			Expect(*updatedMachine.Status.ProviderID).To(ContainSubstring(instanceID))
		})
	})

	Context("When instance is ready", func() {
		It("should mark machine as ready", func() {
			instanceID := uuid.New().String()
			status := nico.InstanceStatus("Ready")

			mockClient := &testutil.MockNcxInfraClient{
				GetInstanceFunc: func(ctx context.Context, org, id string) (*nico.Instance, *http.Response, error) {
					Expect(id).To(Equal(instanceID))
					return &nico.Instance{
						Id:        &instanceID,
						Name:      testutil.Ptr(machineName),
						MachineId: *nico.NewNullableString(testutil.Ptr(uuid.New().String())),
						Status:    &status,
						Interfaces: []nico.Interface{
							{IpAddresses: []string{"10.0.1.10"}},
						},
					}, testutil.MockHTTPResponse(200), nil
				},
			}

			nvidiaCarbideMachine.Finalizers = []string{NcxInfraMachineFinalizer}
			nvidiaCarbideMachine.Status = infrastructurev1.NcxInfraMachineStatus{
				InstanceID: instanceID,
			}

			scheme := newTestScheme()
			k8sClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(cluster, machine, nvidiaCarbideCluster, nvidiaCarbideMachine, credsSecret, bootstrapSecret).
				WithStatusSubresource(
					&infrastructurev1.NcxInfraMachine{},
					&infrastructurev1.NcxInfraCluster{},
					&clusterv1.Machine{},
				).
				Build()

			reconciler := &NcxInfraMachineReconciler{
				Client:              k8sClient,
				Scheme:              scheme,
				NcxInfraClient: mockClient,
				OrgName:             orgName,
			}

			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Requeue).To(BeFalse()) //nolint:staticcheck // checking Requeue field
			Expect(result.RequeueAfter).To(BeZero())

			updatedMachine := &infrastructurev1.NcxInfraMachine{}
			Expect(k8sClient.Get(ctx, namespacedName, updatedMachine)).To(Succeed())
			Expect(updatedMachine.Status.Ready).To(BeTrue())
			Expect(updatedMachine.Status.Addresses).To(HaveLen(1))
			Expect(updatedMachine.Status.Addresses[0].Address).To(Equal("10.0.1.10"))
		})
	})

	Context("When instance is still provisioning", func() {
		It("should requeue after 30 seconds", func() {
			instanceID := uuid.New().String()
			status := nico.InstanceStatus("Provisioning")

			mockClient := &testutil.MockNcxInfraClient{
				GetInstanceFunc: func(ctx context.Context, org, id string) (*nico.Instance, *http.Response, error) {
					return &nico.Instance{
						Id:        &instanceID,
						MachineId: *nico.NewNullableString(testutil.Ptr(uuid.New().String())),
						Status:    &status,
					}, testutil.MockHTTPResponse(200), nil
				},
			}

			nvidiaCarbideMachine.Finalizers = []string{NcxInfraMachineFinalizer}
			nvidiaCarbideMachine.Status = infrastructurev1.NcxInfraMachineStatus{
				InstanceID: instanceID,
			}

			scheme := newTestScheme()
			k8sClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(cluster, machine, nvidiaCarbideCluster, nvidiaCarbideMachine, credsSecret, bootstrapSecret).
				WithStatusSubresource(
					&infrastructurev1.NcxInfraMachine{},
					&infrastructurev1.NcxInfraCluster{},
					&clusterv1.Machine{},
				).
				Build()

			reconciler := &NcxInfraMachineReconciler{
				Client:              k8sClient,
				Scheme:              scheme,
				NcxInfraClient: mockClient,
				OrgName:             orgName,
			}

			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).NotTo(BeZero())
		})
	})

	Context("When deleting a machine", func() {
		It("should delete instance and remove finalizer", func() {
			instanceID := uuid.New().String()
			deleteInstanceCalled := false

			mockClient := &testutil.MockNcxInfraClient{
				DeleteInstanceFunc: func(ctx context.Context, org, id string) (*http.Response, error) {
					deleteInstanceCalled = true
					Expect(id).To(Equal(instanceID))
					return testutil.MockHTTPResponse(200), nil
				},
			}

			// Test reconcileDelete directly to avoid fake client issues with DeletionTimestamp
			machineScope := &scope.MachineScope{
				Cluster:              cluster,
				Machine:              machine,
				NcxInfraCluster: nvidiaCarbideCluster,
				NcxInfraClient:  mockClient,
				OrgName:              orgName,
				NcxInfraMachine: &infrastructurev1.NcxInfraMachine{
					ObjectMeta: metav1.ObjectMeta{
						Name:       machineName,
						Namespace:  clusterNamespace,
						Finalizers: []string{NcxInfraMachineFinalizer},
					},
					Status: infrastructurev1.NcxInfraMachineStatus{
						InstanceID: instanceID,
					},
				},
			}

			reconciler := &NcxInfraMachineReconciler{
				Scheme:              newTestScheme(),
				NcxInfraClient: mockClient,
				OrgName:             orgName,
			}

			result, err := reconciler.reconcileDelete(ctx, machineScope)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Requeue).To(BeFalse()) //nolint:staticcheck // checking Requeue field
			Expect(deleteInstanceCalled).To(BeTrue())
			Expect(machineScope.NcxInfraMachine.Finalizers).NotTo(ContainElement(NcxInfraMachineFinalizer))
		})

		It("should handle 404 gracefully during deletion", func() {
			instanceID := uuid.New().String()

			mockClient := &testutil.MockNcxInfraClient{
				DeleteInstanceFunc: func(ctx context.Context, org, id string) (*http.Response, error) {
					return testutil.MockHTTPResponse(404), fmt.Errorf("not found")
				},
			}

			machineScope := &scope.MachineScope{
				Cluster:              cluster,
				Machine:              machine,
				NcxInfraCluster: nvidiaCarbideCluster,
				NcxInfraClient:  mockClient,
				OrgName:              orgName,
				NcxInfraMachine: &infrastructurev1.NcxInfraMachine{
					ObjectMeta: metav1.ObjectMeta{
						Name:       machineName,
						Namespace:  clusterNamespace,
						Finalizers: []string{NcxInfraMachineFinalizer},
					},
					Status: infrastructurev1.NcxInfraMachineStatus{
						InstanceID: instanceID,
					},
				},
			}

			reconciler := &NcxInfraMachineReconciler{
				Scheme:              newTestScheme(),
				NcxInfraClient: mockClient,
				OrgName:             orgName,
			}

			result, err := reconciler.reconcileDelete(ctx, machineScope)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Requeue).To(BeFalse()) //nolint:staticcheck // checking Requeue field
		})
	})

	Context("When bootstrap data is not ready", func() {
		It("should requeue", func() {
			machine.Spec.Bootstrap.DataSecretName = nil

			scheme := newTestScheme()
			k8sClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(cluster, machine, nvidiaCarbideCluster, nvidiaCarbideMachine, credsSecret).
				WithStatusSubresource(
					&infrastructurev1.NcxInfraMachine{},
					&infrastructurev1.NcxInfraCluster{},
					&clusterv1.Machine{},
				).
				Build()

			reconciler := &NcxInfraMachineReconciler{
				Client: k8sClient,
				Scheme: scheme,
			}

			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).NotTo(BeZero())
		})
	})

	Context("When an instance with the same name already exists", func() {
		It("should reuse the existing instance", func() {
			existingInstanceID := uuid.New().String()
			status := nico.InstanceStatus("Ready")
			physMachineID := uuid.New().String()

			createInstanceCalled := false
			mockClient := &testutil.MockNcxInfraClient{
				GetAllInstanceFunc: func(ctx context.Context, org string) ([]nico.Instance, *http.Response, error) {
					return []nico.Instance{
						{
							Id:        &existingInstanceID,
							Name:      testutil.Ptr(machineName),
							MachineId: *nico.NewNullableString(&physMachineID),
							Status:    &status,
						},
					}, testutil.MockHTTPResponse(200), nil
				},
				GetInstanceFunc: func(ctx context.Context, org, id string) (*nico.Instance, *http.Response, error) {
					return &nico.Instance{
						Id:        &existingInstanceID,
						Name:      testutil.Ptr(machineName),
						MachineId: *nico.NewNullableString(&physMachineID),
						Status:    &status,
						Interfaces: []nico.Interface{
							{IpAddresses: []string{"10.0.1.10"}},
						},
					}, testutil.MockHTTPResponse(200), nil
				},
				CreateInstanceFunc: func(ctx context.Context, org string, req nico.InstanceCreateRequest) (*nico.Instance, *http.Response, error) {
					createInstanceCalled = true
					return nil, nil, fmt.Errorf("should not be called")
				},
			}

			nvidiaCarbideMachine.Finalizers = []string{NcxInfraMachineFinalizer}

			scheme := newTestScheme()
			k8sClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(cluster, machine, nvidiaCarbideCluster, nvidiaCarbideMachine, credsSecret, bootstrapSecret).
				WithStatusSubresource(
					&infrastructurev1.NcxInfraMachine{},
					&infrastructurev1.NcxInfraCluster{},
					&clusterv1.Machine{},
				).
				Build()

			reconciler := &NcxInfraMachineReconciler{
				Client:              k8sClient,
				Scheme:              scheme,
				NcxInfraClient: mockClient,
				OrgName:             orgName,
			}

			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Requeue).To(BeFalse()) //nolint:staticcheck // checking Requeue field
			Expect(createInstanceCalled).To(BeFalse())

			updatedMachine := &infrastructurev1.NcxInfraMachine{}
			Expect(k8sClient.Get(ctx, namespacedName, updatedMachine)).To(Succeed())
			Expect(updatedMachine.Status.InstanceID).To(Equal(existingInstanceID))
			Expect(updatedMachine.Status.Ready).To(BeTrue())
		})
	})
})
