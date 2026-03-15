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

package v1beta1

import (
	"context"
	"fmt"
	"net"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// +kubebuilder:webhook:path=/validate-infrastructure-cluster-x-k8s-io-v1beta1-nvidiacarbidecluster,mutating=false,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=nvidiacarbideclusters,verbs=create;update,versions=v1beta1,name=vnvidiacarbidecluster.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &NvidiaCarbideCluster{}

func (r *NvidiaCarbideCluster) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		WithValidator(r).
		Complete()
}

func (r *NvidiaCarbideCluster) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	cluster, ok := obj.(*NvidiaCarbideCluster)
	if !ok {
		return nil, fmt.Errorf("expected NvidiaCarbideCluster, got %T", obj)
	}
	return nil, cluster.validateCluster().ToAggregate()
}

func (r *NvidiaCarbideCluster) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldCluster, ok := oldObj.(*NvidiaCarbideCluster)
	if !ok {
		return nil, fmt.Errorf("expected NvidiaCarbideCluster, got %T", oldObj)
	}
	newCluster, ok := newObj.(*NvidiaCarbideCluster)
	if !ok {
		return nil, fmt.Errorf("expected NvidiaCarbideCluster, got %T", newObj)
	}

	var allErrs field.ErrorList

	// Validate the new spec
	allErrs = append(allErrs, newCluster.validateCluster()...)

	// Validate immutable fields
	allErrs = append(allErrs, newCluster.validateImmutableFields(oldCluster)...)

	return nil, allErrs.ToAggregate()
}

func (r *NvidiaCarbideCluster) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (r *NvidiaCarbideCluster) validateCluster() field.ErrorList {
	var allErrs field.ErrorList
	specPath := field.NewPath("spec")

	// Validate VPC name is non-empty
	if r.Spec.VPC.Name == "" {
		allErrs = append(allErrs, field.Required(
			specPath.Child("vpc", "name"),
			"VPC name must not be empty"))
	}

	// Validate network virtualization type
	nvt := r.Spec.VPC.NetworkVirtualizationType
	if nvt != "" && nvt != "ETHERNET_VIRTUALIZER" && nvt != "FNN" {
		allErrs = append(allErrs, field.NotSupported(
			specPath.Child("vpc", "networkVirtualizationType"),
			nvt, []string{"ETHERNET_VIRTUALIZER", "FNN"}))
	}

	// Validate site reference: at least one of name or ID must be set
	if r.Spec.SiteRef.Name == "" && r.Spec.SiteRef.ID == "" {
		allErrs = append(allErrs, field.Required(
			specPath.Child("siteRef"),
			"at least one of name or id must be specified"))
	}

	// Validate tenant ID
	if r.Spec.TenantID == "" {
		allErrs = append(allErrs, field.Required(
			specPath.Child("tenantID"),
			"tenant ID must not be empty"))
	}

	// Validate subnets
	if len(r.Spec.Subnets) == 0 {
		allErrs = append(allErrs, field.Required(
			specPath.Child("subnets"),
			"at least one subnet must be specified"))
	}

	for i, subnet := range r.Spec.Subnets {
		subnetPath := specPath.Child("subnets").Index(i)

		if subnet.Name == "" {
			allErrs = append(allErrs, field.Required(
				subnetPath.Child("name"),
				"subnet name must not be empty"))
		}

		// Validate CIDR format
		if subnet.CIDR != "" {
			if _, _, err := net.ParseCIDR(subnet.CIDR); err != nil {
				allErrs = append(allErrs, field.Invalid(
					subnetPath.Child("cidr"),
					subnet.CIDR,
					fmt.Sprintf("invalid CIDR: %v", err)))
			}
		} else {
			allErrs = append(allErrs, field.Required(
				subnetPath.Child("cidr"),
				"CIDR must not be empty"))
		}
	}

	// Validate VPC Prefixes
	for i, prefix := range r.Spec.VPCPrefixes {
		prefixPath := specPath.Child("vpcPrefixes").Index(i)

		if prefix.Name == "" {
			allErrs = append(allErrs, field.Required(
				prefixPath.Child("name"),
				"VPC prefix name must not be empty"))
		}

		if prefix.CIDR != "" {
			if _, _, err := net.ParseCIDR(prefix.CIDR); err != nil {
				allErrs = append(allErrs, field.Invalid(
					prefixPath.Child("cidr"),
					prefix.CIDR,
					fmt.Sprintf("invalid CIDR: %v", err)))
			}
		} else {
			allErrs = append(allErrs, field.Required(
				prefixPath.Child("cidr"),
				"CIDR must not be empty"))
		}
	}

	// Validate authentication
	if r.Spec.Authentication.SecretRef.Name == "" {
		allErrs = append(allErrs, field.Required(
			specPath.Child("authentication", "secretRef", "name"),
			"credentials secret name must not be empty"))
	}

	if len(allErrs) > 0 {
		return allErrs
	}
	return nil
}

func (r *NvidiaCarbideCluster) validateImmutableFields(old *NvidiaCarbideCluster) field.ErrorList {
	var allErrs field.ErrorList
	specPath := field.NewPath("spec")

	// SiteRef is immutable after creation
	if old.Spec.SiteRef.Name != r.Spec.SiteRef.Name {
		allErrs = append(allErrs, field.Forbidden(
			specPath.Child("siteRef", "name"),
			"field is immutable after creation"))
	}
	if old.Spec.SiteRef.ID != r.Spec.SiteRef.ID {
		allErrs = append(allErrs, field.Forbidden(
			specPath.Child("siteRef", "id"),
			"field is immutable after creation"))
	}

	// TenantID is immutable
	if old.Spec.TenantID != r.Spec.TenantID {
		allErrs = append(allErrs, field.Forbidden(
			specPath.Child("tenantID"),
			"field is immutable after creation"))
	}

	// VPC name is immutable
	if old.Spec.VPC.Name != r.Spec.VPC.Name {
		allErrs = append(allErrs, field.Forbidden(
			specPath.Child("vpc", "name"),
			"field is immutable after creation"))
	}

	if len(allErrs) > 0 {
		return allErrs
	}
	return nil
}

// Ensure the webhook returns proper API errors
func init() {
	_ = apierrors.NewInvalid
}
