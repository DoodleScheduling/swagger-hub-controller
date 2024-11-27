package v1beta1

import (
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type SwaggerSpecification struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SwaggerSpecificationSpec   `json:"spec,omitempty"`
	Status SwaggerSpecificationStatus `json:"status,omitempty"`
}

// SwaggerSpecificationStatus defines the observed state of SwaggerSpecification
type SwaggerSpecificationStatus struct {
	// Conditions holds the conditions for the SwaggerSpecification.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the last generation reconciled by the controller
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// SubResourceCatalog holds references to all sub resources
	SubResourceCatalog []ResourceLookupReference `json:"subResourceCatalog,omitempty"`
}

// ResourceLookupReference metadata to lookup another resource
type ResourceLookupReference struct {
	Kind       string `json:"kind,omitempty"`
	Name       string `json:"name,omitempty"`
	Error      string `json:"error,omitempty"`
	APIVersion string `json:"apiVersion,omitempty"`
}

// SwaggerSpecificationList contains a list of SwaggerSpecification.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type SwaggerSpecificationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SwaggerSpecification `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SwaggerSpecification{}, &SwaggerSpecificationList{})
}

// SwaggerSpecificationSpec defines the desired state of SwaggerSpecification.
// +k8s:openapi-gen=true
type SwaggerSpecificationSpec struct {
	// Suspend reconciliation
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// NamespaceSelector defines a selector to select namespaces where definitions are looked up
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`

	// DefinitionSelector defines a selector to select swagger definitions associated with this hub
	DefinitionSelector *metav1.LabelSelector `json:"definitionSelector,omitempty"`

	Info    Info    `json:"info,omitempty"`
	Servers Servers `json:"servers,omitempty"`
}

type Info struct {
	Extensions map[string]json.RawMessage `json:"-"`

	Title          string  `json:"title,omitempty" `
	Description    string  `json:"description,omitempty"`
	TermsOfService string  `json:"termsOfService,omitempty"`
	Contact        Contact `json:"contact,omitempty"`
	License        License `json:"license,omitempty"`
	Version        string  `json:"version"`
}

type License struct {
	Extensions map[string]json.RawMessage `json:"-"`

	Name string `json:"name,omitempty"`
	URL  string `json:"url,omitempty"`
}

type Contact struct {
	Extensions map[string]json.RawMessage `json:"-"`

	Name  string `json:"name,omitempty"`
	URL   string `json:"url,omitempty"`
	Email string `json:"email,omitempty"`
}

type Servers []*Server

type Server struct {
	Extensions map[string]json.RawMessage `json:"-"`

	URL         string                     `json:"url,omitempty"`
	Description string                     `json:"description,omitempty"`
	Variables   map[string]*ServerVariable `json:"variables,omitempty"`
}

type ServerVariable struct {
	Extensions map[string]json.RawMessage `json:"-"`

	Enum        []string `json:"enum,omitempty"`
	Default     string   `json:"default,omitempty"`
	Description string   `json:"description,omitempty"`
}

func SwaggerSpecificationReconciling(realm SwaggerSpecification, status metav1.ConditionStatus, reason, message string) SwaggerSpecification {
	setResourceCondition(&realm, ConditionReconciling, status, reason, message, realm.ObjectMeta.Generation)
	return realm
}

func SwaggerSpecificationReady(realm SwaggerSpecification, status metav1.ConditionStatus, reason, message string) SwaggerSpecification {
	setResourceCondition(&realm, ConditionReady, status, reason, message, realm.ObjectMeta.Generation)
	return realm
}

// GetStatusConditions returns a pointer to the Status.Conditions slice
func (in *SwaggerSpecification) GetStatusConditions() *[]metav1.Condition {
	return &in.Status.Conditions
}

func (in *SwaggerSpecification) GetConditions() []metav1.Condition {
	return in.Status.Conditions
}

func (in *SwaggerSpecification) SetConditions(conditions []metav1.Condition) {
	in.Status.Conditions = conditions
}
