/*


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
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status",description=""
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].message",description=""
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description=""

// SwaggerHub is the Schema for the SwaggerHubs API
type SwaggerHub struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SwaggerHubSpec   `json:"spec,omitempty"`
	Status SwaggerHubStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SwaggerHubList contains a list of SwaggerHub
type SwaggerHubList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SwaggerHub `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SwaggerHub{}, &SwaggerHubList{})
}

// SwaggerHubSpec defines the desired state of SwaggerHub
type SwaggerHubSpec struct {
	// +required
	DeploymentTemplate *DeploymentTemplate `json:"deploymentTemplate,omitempty"`

	// Suspend reconciliation
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// DefinitionSelector defines a selector to select swagger definitions associated with this hub
	DefinitionSelector *metav1.LabelSelector `json:"definitionSelector,omitempty"`

	// NamespaceSelector defines a selector to select namespaces where definitions are looked up
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`
}

type DeploymentTemplate struct {
	// Standard object's metadata.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#metadata
	// +optional
	ObjectMetadata `json:"metadata,omitempty"`

	// Specification of the desired behavior of the pod.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#spec-and-status
	// +optional
	Spec DeploymentSpec `json:"spec,omitempty"`
}

type DeploymentSpec struct {
	// Number of desired pods. This is a pointer to distinguish between explicit
	// zero and not specified. Defaults to 1.
	// +optional
	Replicas *int32 `json:"replicas,omitempty" protobuf:"varint,1,opt,name=replicas"`

	// Template describes the pods that will be created.
	// The only allowed template.spec.restartPolicy value is "Always".
	Template v1.PodTemplateSpec `json:"template" protobuf:"bytes,3,opt,name=template"`

	// The deployment strategy to use to replace existing pods with new ones.
	// +optional
	// +patchStrategy=retainKeys
	Strategy appsv1.DeploymentStrategy `json:"strategy,omitempty" patchStrategy:"retainKeys" protobuf:"bytes,4,opt,name=strategy"`

	// Minimum number of seconds for which a newly created pod should be ready
	// without any of its container crashing, for it to be considered available.
	// Defaults to 0 (pod will be considered available as soon as it is ready)
	// +optional
	MinReadySeconds int32 `json:"minReadySeconds,omitempty" protobuf:"varint,5,opt,name=minReadySeconds"`

	// The number of old ReplicaSets to retain to allow rollback.
	// This is a pointer to distinguish between explicit zero and not specified.
	// Defaults to 10.
	// +optional
	RevisionHistoryLimit *int32 `json:"revisionHistoryLimit,omitempty" protobuf:"varint,6,opt,name=revisionHistoryLimit"`

	// Indicates that the deployment is paused.
	// +optional
	Paused bool `json:"paused,omitempty" protobuf:"varint,7,opt,name=paused"`

	// The maximum time in seconds for a deployment to make progress before it
	// is considered to be failed. The deployment controller will continue to
	// process failed deployments and a condition with a ProgressDeadlineExceeded
	// reason will be surfaced in the deployment status. Note that progress will
	// not be estimated during the time a deployment is paused. Defaults to 600s.
	ProgressDeadlineSeconds *int32 `json:"progressDeadlineSeconds,omitempty" protobuf:"varint,9,opt,name=progressDeadlineSeconds"`
}

type ObjectMetadata struct {
	// Map of string keys and values that can be used to organize and categorize
	// (scope and select) objects. May match selectors of replication controllers
	// and services.
	// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/labels
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations is an unstructured key value map stored with a resource that may be
	// set by external tools to store and retrieve arbitrary metadata. They are not
	// queryable and should be preserved when modifying objects.
	// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/annotations
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// SwaggerHubStatus defines the observed state of SwaggerHub
type SwaggerHubStatus struct {
	// Conditions holds the conditions for the SwaggerHub.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// DeploymentName is the currently rolled out swagger-ui
	DeploymentName string `json:"deploymentName,omitempty"`

	// DeploymentHash is used to determine wether a new rollout is required
	DeploymentHash string `json:"deploymentHash,omitempty"`

	// DeploymentGeneration is used to verify if the deployment did not change by other means
	DeploymentGeneration int64 `json:"deploymentHash,omitempty"`

	// ObservedGeneration is the last generation reconciled by the controller
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// SubResourceCatalog holds references to all sub resources including KeycloakClient and KeycloakUser associated with this realm
	SubResourceCatalog []ResourceReference `json:"subResourceCatalog,omitempty"`
}

// ResourceReference metadata to lookup another resource
type ResourceReference struct {
	Kind       string `json:"kind,omitempty"`
	Name       string `json:"name,omitempty"`
	APIVersion string `json:"apiVersion,omitempty"`
}

func SwaggerHubReconciling(realm SwaggerHub, status metav1.ConditionStatus, reason, message string) SwaggerHub {
	setResourceCondition(&realm, ConditionReconciling, status, reason, message, realm.ObjectMeta.Generation)
	return realm
}

func SwaggerHubReady(realm SwaggerHub, status metav1.ConditionStatus, reason, message string) SwaggerHub {
	setResourceCondition(&realm, ConditionReady, status, reason, message, realm.ObjectMeta.Generation)
	return realm
}

// GetStatusConditions returns a pointer to the Status.Conditions slice
func (in *SwaggerHub) GetStatusConditions() *[]metav1.Condition {
	return &in.Status.Conditions
}

func (in *SwaggerHub) GetConditions() []metav1.Condition {
	return in.Status.Conditions
}

func (in *SwaggerHub) SetConditions(conditions []metav1.Condition) {
	in.Status.Conditions = conditions
}
