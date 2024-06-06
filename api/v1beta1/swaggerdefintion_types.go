package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type SwaggerDefinition struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec SwaggerDefinitionSpec `json:"spec,omitempty"`
}

// SwaggerDefinitionList contains a list of SwaggerDefinition.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type SwaggerDefinitionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SwaggerDefinition `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SwaggerDefinition{}, &SwaggerDefinitionList{})
}

// SwaggerDefinitionSpec defines the desired state of SwaggerDefinition.
// +k8s:openapi-gen=true
type SwaggerDefinitionSpec struct {
	URL *string `json:"url,omitempty"`
}
