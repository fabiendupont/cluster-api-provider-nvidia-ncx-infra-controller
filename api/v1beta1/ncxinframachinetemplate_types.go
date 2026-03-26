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

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// NcxInfraMachineTemplateSpec defines the desired state of NcxInfraMachineTemplate
type NcxInfraMachineTemplateSpec struct {
	// Template contains the NcxInfraMachine template specification
	// +required
	Template NcxInfraMachineTemplateResource `json:"template"`
}

// NcxInfraMachineTemplateResource describes the data needed to create a NcxInfraMachine from a template
type NcxInfraMachineTemplateResource struct {
	// Standard object's metadata
	// +optional
	ObjectMeta metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec is the specification of the desired behavior of the machine
	// +required
	Spec NcxInfraMachineSpec `json:"spec"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=ncxinframachinetemplates,scope=Namespaced,categories=cluster-api
// +kubebuilder:storageversion

// NcxInfraMachineTemplate is the Schema for the ncxinframachinetemplates API
type NcxInfraMachineTemplate struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of NcxInfraMachineTemplate
	// +required
	Spec NcxInfraMachineTemplateSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// NcxInfraMachineTemplateList contains a list of NcxInfraMachineTemplate
type NcxInfraMachineTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []NcxInfraMachineTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NcxInfraMachineTemplate{}, &NcxInfraMachineTemplateList{})
}
