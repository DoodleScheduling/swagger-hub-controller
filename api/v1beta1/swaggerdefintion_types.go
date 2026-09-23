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

	Status SwaggerDefinitionStatus `json:"status,omitempty"`
}

// SwaggerDefinitionList contains a list of SwaggerDefinition.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type SwaggerDefinitionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SwaggerDefinition `json:"items"`
}

func init() {
	objectTypes = append(objectTypes, &SwaggerDefinition{}, &SwaggerDefinitionList{})
}

// SwaggerDefinitionSpec defines the desired state of SwaggerDefinition.
// +k8s:openapi-gen=true
type SwaggerDefinitionSpec struct {
	URL *string `json:"url,omitempty"`

	// Suspend reconciliation
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// Interval for reconciliation
	// +optional
	Interval metav1.Duration `json:"interval,omitempty"`

	// Auth configures how the controller authenticates while fetching the
	// definition from the url.
	// +optional
	Auth *DefinitionAuth `json:"auth,omitempty"`
}

// DefinitionAuth defines the authentication used to fetch a definition.
type DefinitionAuth struct {
	// Basic configures http basic authentication.
	// +optional
	Basic *BasicAuth `json:"basic,omitempty"`
}

// BasicAuth defines http basic authentication credentials.
type BasicAuth struct {
	// Username is a static username. If set it takes precedence over the username
	// looked up from secretRef.usernameField.
	// +optional
	Username string `json:"username,omitempty"`

	// SecretRef references a secret in the same namespace as the SwaggerDefinition
	// which holds the credentials.
	// +required
	SecretRef LocalSecretReference `json:"secretRef"`

	// AllowInsecure permits sending the credentials to a plain http:// url.
	// +optional
	AllowInsecure bool `json:"allowInsecure,omitempty"`
}

// LocalSecretReference references a secret within the same namespace.
type LocalSecretReference struct {
	// Name of the secret.
	// +required
	Name string `json:"name"`

	// UsernameField is the secret key which holds the username. It is ignored if
	// a static username is configured.
	// +kubebuilder:default:=username
	// +optional
	UsernameField string `json:"usernameField,omitempty"`

	// PasswordField is the secret key which holds the password.
	// +kubebuilder:default:=password
	// +optional
	PasswordField string `json:"passwordField,omitempty"`

	// Suspend reconciliation
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// Interval for reconciliation
	// +optional
	Interval metav1.Duration `json:"interval,omitempty"`
}

type SwaggerDefinitionStatus struct {
	// Conditions holds the conditions for the SwaggerDefinition.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the last generation reconciled by the controller
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

func SwaggerDefinitionReconciling(definition SwaggerDefinition, status metav1.ConditionStatus, reason, message string) SwaggerDefinition {
	setResourceCondition(&definition, ConditionReconciling, status, reason, message, definition.Generation)
	return definition
}

func SwaggerDefinitionReady(definition SwaggerDefinition, status metav1.ConditionStatus, reason, message string) SwaggerDefinition {
	setResourceCondition(&definition, ConditionReady, status, reason, message, definition.Generation)
	return definition
}

// GetStatusConditions returns a pointer to the Status.Conditions slice
func (in *SwaggerDefinition) GetStatusConditions() *[]metav1.Condition {
	return &in.Status.Conditions
}

func (in *SwaggerDefinition) GetConditions() []metav1.Condition {
	return in.Status.Conditions
}

func (in *SwaggerDefinition) SetConditions(conditions []metav1.Condition) {
	in.Status.Conditions = conditions
}
