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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NcxInfraClusterTemplateSpec defines the desired state of NcxInfraClusterTemplate
type NcxInfraClusterTemplateSpec struct {
	// Template contains the NcxInfraCluster template specification
	// +required
	Template NcxInfraClusterTemplateResource `json:"template"`
}

// NcxInfraClusterTemplateResource describes the data needed to create a NcxInfraCluster from a template
type NcxInfraClusterTemplateResource struct {
	// Standard object's metadata
	// +optional
	ObjectMeta metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec is the specification of the desired behavior of the cluster
	// +required
	Spec NcxInfraClusterSpec `json:"spec"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=ncxinfraclustertemplates,scope=Namespaced,categories=cluster-api
// +kubebuilder:storageversion

// NcxInfraClusterTemplate is the Schema for the ncxinfraclustertemplates API
type NcxInfraClusterTemplate struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of NcxInfraClusterTemplate
	// +required
	Spec NcxInfraClusterTemplateSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// NcxInfraClusterTemplateList contains a list of NcxInfraClusterTemplate
type NcxInfraClusterTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []NcxInfraClusterTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NcxInfraClusterTemplate{}, &NcxInfraClusterTemplateList{})
}
