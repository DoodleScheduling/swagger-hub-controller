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

package controllers

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1beta1 "github.com/DoodleScheduling/swagger-hub-controller/api/v1beta1"
	"github.com/DoodleScheduling/swagger-hub-controller/internal/merge"
)

// +kubebuilder:rbac:groups=swagger.infra.doodle.com,resources=swaggerhubs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=swagger.infra.doodle.com,resources=swaggerhubs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=swagger.infra.doodle.com,resources=swaggerdefinitions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=swagger.infra.doodle.com,resources=swaggerspecifications,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;update;patch;delete;watch;list
// +kubebuilder:rbac:groups="",resources=services,verbs=get;update;patch;delete;watch;list
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// SwaggerHub reconciles a SwaggerHub object
type SwaggerHubReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

type SwaggerHubReconcilerOptions struct {
	MaxConcurrentReconciles int
}

// SetupWithManager adding controllers
func (r *SwaggerHubReconciler) SetupWithManager(mgr ctrl.Manager, opts SwaggerHubReconcilerOptions) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1beta1.SwaggerHub{}, builder.WithPredicates(
			predicate.GenerationChangedPredicate{},
		)).
		Watches(
			&infrav1beta1.SwaggerDefinition{},
			handler.EnqueueRequestsFromMapFunc(r.requestsForChangeBySelector),
		).
		Watches(
			&infrav1beta1.SwaggerSpecification{},
			handler.EnqueueRequestsFromMapFunc(r.requestsForChangeBySelector),
		).
		Watches(
			&corev1.Service{},
			handler.EnqueueRequestForOwner(mgr.GetScheme(), mgr.GetRESTMapper(), &infrav1beta1.SwaggerHub{}, handler.OnlyControllerOwner()),
		).
		WithOptions(controller.Options{MaxConcurrentReconciles: opts.MaxConcurrentReconciles}).
		Complete(r)
}

func (r *SwaggerHubReconciler) requestsForChangeBySelector(ctx context.Context, o client.Object) []reconcile.Request {
	var list infrav1beta1.SwaggerHubList
	if err := r.List(ctx, &list, client.InNamespace(o.GetNamespace())); err != nil {
		return nil
	}

	var reqs []reconcile.Request
	for _, hub := range list.Items {
		labelSel, err := metav1.LabelSelectorAsSelector(hub.Spec.DefinitionSelector)
		if err != nil {
			r.Log.Error(err, "can not select resourceSelector selectors")
			continue
		}

		if labelSel.Matches(labels.Set(o.GetLabels())) {
			r.Log.V(1).Info("referenced resource from a SwaggerHub changed detected", "namespace", hub.GetNamespace(), "hub-name", hub.GetName())
			reqs = append(reqs, reconcile.Request{NamespacedName: objectKey(&hub)})
		}
	}

	return reqs
}

// Reconcile SwaggerHubs
func (r *SwaggerHubReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("namespace", req.Namespace, "name", req.NamespacedName)
	logger.Info("reconciling SwaggerHub")

	// Fetch the SwaggerHub instance
	hub := infrav1beta1.SwaggerHub{}

	err := r.Client.Get(ctx, req.NamespacedName, &hub)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	if hub.Spec.Suspend {
		return ctrl.Result{}, nil
	}

	hub, result, err := r.reconcile(ctx, hub)
	hub.Status.ObservedGeneration = hub.GetGeneration()

	if err != nil {
		logger.Error(err, "reconcile error occurred")
		hub = infrav1beta1.SwaggerHubReady(hub, metav1.ConditionFalse, "ReconciliationFailed", err.Error())
		r.Recorder.Event(&hub, "Normal", "error", err.Error())
		result.Requeue = true
	}

	// Update status after reconciliation.
	if err := r.patchStatus(ctx, &hub); err != nil {
		logger.Error(err, "unable to update status after reconciliation")
		return ctrl.Result{Requeue: true}, err
	}

	return result, err
}

type apiURL struct {
	Name string `json:"name,omitempty"`
	URL  string `json:"url,omitempty"`
}

func (r *SwaggerHubReconciler) reconcile(ctx context.Context, hub infrav1beta1.SwaggerHub) (infrav1beta1.SwaggerHub, ctrl.Result, error) {
	hub.Status.SubResourceCatalog = []infrav1beta1.ResourceReference{}

	hub, definitions, err := r.extendhubWithDefinitions(ctx, hub)
	if err != nil {
		return hub, ctrl.Result{}, err
	}

	hub, specifications, err := r.extendhubWithSpecifications(ctx, hub)
	if err != nil {
		return hub, ctrl.Result{}, err
	}

	var (
		gid          int64 = 10000
		uid          int64 = 10000
		runAsNonRoot bool  = true
		replicas     int32 = 1
	)

	controllerOwner := true
	template := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("swagger-ui-%s", hub.Name),
			Namespace: hub.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					Name:       hub.Name,
					APIVersion: hub.APIVersion,
					Kind:       hub.Kind,
					UID:        hub.UID,
					Controller: &controllerOwner,
				},
			},
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						RunAsUser:    &uid,
						RunAsGroup:   &gid,
						RunAsNonRoot: &runAsNonRoot,
					},
				},
			},
		},
	}

	if hub.Spec.DeploymentTemplate != nil {
		template.ObjectMeta.Labels = hub.Spec.DeploymentTemplate.Labels
		template.ObjectMeta.Annotations = hub.Spec.DeploymentTemplate.Annotations
		hub.Spec.DeploymentTemplate.Spec.Template.DeepCopyInto(&template.Spec.Template)
		template.Spec.MinReadySeconds = hub.Spec.DeploymentTemplate.Spec.MinReadySeconds
		template.Spec.Paused = hub.Spec.DeploymentTemplate.Spec.Paused
		template.Spec.ProgressDeadlineSeconds = hub.Spec.DeploymentTemplate.Spec.ProgressDeadlineSeconds
		template.Spec.Replicas = hub.Spec.DeploymentTemplate.Spec.Replicas
		template.Spec.RevisionHistoryLimit = hub.Spec.DeploymentTemplate.Spec.RevisionHistoryLimit
		template.Spec.Strategy = hub.Spec.DeploymentTemplate.Spec.Strategy
	}

	if template.ObjectMeta.Labels == nil {
		template.ObjectMeta.Labels = make(map[string]string)
	}

	if template.Spec.Template.ObjectMeta.Labels == nil {
		template.Spec.Template.ObjectMeta.Labels = make(map[string]string)
	}

	template.Spec.Selector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"app.kubernetes.io/instance": "swagger-ui",
			"app.kubernetes.io/name":     "swagger-ui",
			"swagger-hub-controller/hub": hub.Name,
		},
	}

	if template.Spec.Replicas == nil {
		template.Spec.Replicas = &replicas
	}

	template.Spec.Template.ObjectMeta.Labels["app.kubernetes.io/instance"] = "swagger-ui"
	template.Spec.Template.ObjectMeta.Labels["app.kubernetes.io/name"] = "swagger-ui"
	template.Spec.Template.ObjectMeta.Labels["swagger-hub-controller/hub"] = hub.Name
	template.ObjectMeta.Labels["app.kubernetes.io/instance"] = "swagger-ui"
	template.ObjectMeta.Labels["app.kubernetes.io/name"] = "swagger-ui"
	template.ObjectMeta.Labels["swagger-hub-controller/hub"] = hub.Name

	if template.Annotations == nil {
		template.Annotations = make(map[string]string)
	}

	var apiURLs []apiURL
	for _, definition := range definitions {
		apiURLs = append(apiURLs, apiURL{
			Name: fmt.Sprintf("%s:%s", definition.Name, definition.Namespace),
			URL:  *definition.Spec.URL,
		})
	}

	containers := []corev1.Container{
		{
			Name:  "swagger-ui",
			Image: "swaggerapi/swagger-ui:latest",
			LivenessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Port: intstr.IntOrString{StrVal: "http", Type: intstr.String},
						Path: "/",
					},
				},
			},
			ReadinessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Port: intstr.IntOrString{StrVal: "http", Type: intstr.String},
						Path: "/",
					},
				},
			},
			Ports: []corev1.ContainerPort{
				{
					Name:          "http",
					ContainerPort: 8080,
				},
			},
		},
	}

	frontendURL := "http://localhost"
	if hub.Spec.FrontendURL != "" {
		frontendURL = hub.Spec.FrontendURL
	}

	for _, specification := range specifications {
		template.Spec.Template.Spec.Volumes = append(template.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: fmt.Sprintf("swagger-specification-%s", specification.Name),
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: fmt.Sprintf("swagger-specification-%s", specification.Name),
					},
				},
			},
		})

		containers[0].VolumeMounts = append(containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      fmt.Sprintf("swagger-specification-%s", specification.Name),
			ReadOnly:  true,
			MountPath: fmt.Sprintf("/usr/share/nginx/html/specifications/%s", specification.Name),
		})

		apiURLs = append(apiURLs, apiURL{
			Name: fmt.Sprintf("%s:%s", specification.Name, specification.Namespace),
			URL:  fmt.Sprintf("%s/specifications/%s/specification.json", frontendURL, specification.Name),
		})
	}

	if len(apiURLs) > 0 {
		b, err := json.Marshal(apiURLs)
		if err != nil {
			return hub, ctrl.Result{}, err
		}

		env := corev1.EnvVar{
			Name:  "API_URLS",
			Value: string(b),
		}

		containers[0].Env = append(containers[0].Env, env)
	}

	containers, err = merge.MergePatchContainers(containers, template.Spec.Template.Spec.Containers)
	if err != nil {
		return hub, ctrl.Result{}, err
	}

	template.Spec.Template.Spec.Containers = containers
	r.Log.Info("create swagger-ui deployment", "deployment-name", template.Name)

	svcTemplate := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("swagger-ui-%s", hub.Name),
			Namespace: hub.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					Name:       hub.Name,
					APIVersion: hub.APIVersion,
					Kind:       hub.Kind,
					UID:        hub.UID,
					Controller: &controllerOwner,
				},
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       80,
					TargetPort: intstr.IntOrString{StrVal: "http", Type: intstr.String},
				},
			},
			Selector: map[string]string{
				"app.kubernetes.io/instance": "swagger-ui",
				"app.kubernetes.io/name":     "swagger-ui",
				"swagger-hub-controller/hub": hub.Name,
			},
		},
	}

	var svc corev1.Service
	err = r.Client.Get(ctx, client.ObjectKey{
		Namespace: svcTemplate.Namespace,
		Name:      svcTemplate.Name,
	}, &svc)

	if err != nil && !apierrors.IsNotFound(err) {
		return hub, ctrl.Result{}, err
	}

	if apierrors.IsNotFound(err) {
		if err := r.Client.Create(ctx, svcTemplate); err != nil {
			return hub, ctrl.Result{}, err
		}
	} else {
		if err := r.Client.Update(ctx, svcTemplate); err != nil {
			return hub, ctrl.Result{}, err
		}
	}

	var deployment appsv1.Deployment
	err = r.Client.Get(ctx, client.ObjectKey{
		Namespace: template.Namespace,
		Name:      template.Name,
	}, &deployment)

	if err != nil && !apierrors.IsNotFound(err) {
		return hub, ctrl.Result{}, err
	}

	if apierrors.IsNotFound(err) {
		if err := r.Client.Create(ctx, template); err != nil {
			return hub, ctrl.Result{}, err
		}

	} else {
		if err := r.Client.Update(ctx, template); err != nil {
			return hub, ctrl.Result{}, err
		}
	}

	hub = infrav1beta1.SwaggerHubReady(hub, metav1.ConditionTrue, "ReconciliationSuccessful", fmt.Sprintf("deployment/%s created", template.Name))
	return hub, ctrl.Result{}, nil
}

func (r *SwaggerHubReconciler) extendhubWithSpecifications(ctx context.Context, hub infrav1beta1.SwaggerHub) (infrav1beta1.SwaggerHub, []infrav1beta1.SwaggerSpecification, error) {
	var specifications infrav1beta1.SwaggerSpecificationList
	definitionSelector, err := metav1.LabelSelectorAsSelector(hub.Spec.SpecificationSelector)
	if err != nil {
		return hub, nil, err
	}

	var namespaces corev1.NamespaceList
	if hub.Spec.NamespaceSelector == nil {
		namespaces.Items = append(namespaces.Items, corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: hub.Namespace,
			},
		})
	} else {
		namespaceSelector, err := metav1.LabelSelectorAsSelector(hub.Spec.NamespaceSelector)
		if err != nil {
			return hub, nil, err
		}

		err = r.Client.List(ctx, &namespaces, client.MatchingLabelsSelector{Selector: namespaceSelector})
		if err != nil {
			return hub, nil, err
		}
	}

	for _, namespace := range namespaces.Items {
		var namespacedSpecification infrav1beta1.SwaggerSpecificationList
		err = r.Client.List(ctx, &namespacedSpecification, client.InNamespace(namespace.Name), client.MatchingLabelsSelector{Selector: definitionSelector})
		if err != nil {
			return hub, nil, err
		}

		specifications.Items = append(specifications.Items, namespacedSpecification.Items...)
	}

	slices.SortFunc(specifications.Items, func(a, b infrav1beta1.SwaggerSpecification) int {
		return cmp.Or(
			cmp.Compare(a.Name, b.Name),
			cmp.Compare(a.Namespace, b.Namespace),
		)
	})

	for _, client := range specifications.Items {
		hub.Status.SubResourceCatalog = append(hub.Status.SubResourceCatalog, infrav1beta1.ResourceReference{
			Kind:       client.Kind,
			Name:       client.Name,
			APIVersion: client.APIVersion,
		})
	}

	return hub, specifications.Items, nil
}

func (r *SwaggerHubReconciler) extendhubWithDefinitions(ctx context.Context, hub infrav1beta1.SwaggerHub) (infrav1beta1.SwaggerHub, []infrav1beta1.SwaggerDefinition, error) {
	var definitions infrav1beta1.SwaggerDefinitionList
	definitionSelector, err := metav1.LabelSelectorAsSelector(hub.Spec.DefinitionSelector)
	if err != nil {
		return hub, nil, err
	}

	var namespaces corev1.NamespaceList
	if hub.Spec.NamespaceSelector == nil {
		namespaces.Items = append(namespaces.Items, corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: hub.Namespace,
			},
		})
	} else {
		namespaceSelector, err := metav1.LabelSelectorAsSelector(hub.Spec.NamespaceSelector)
		if err != nil {
			return hub, nil, err
		}

		err = r.Client.List(ctx, &namespaces, client.MatchingLabelsSelector{Selector: namespaceSelector})
		if err != nil {
			return hub, nil, err
		}
	}

	for _, namespace := range namespaces.Items {
		var namespacedDefinitions infrav1beta1.SwaggerDefinitionList
		err = r.Client.List(ctx, &namespacedDefinitions, client.InNamespace(namespace.Name), client.MatchingLabelsSelector{Selector: definitionSelector})
		if err != nil {
			return hub, nil, err
		}

		definitions.Items = append(definitions.Items, namespacedDefinitions.Items...)
	}

	slices.SortFunc(definitions.Items, func(a, b infrav1beta1.SwaggerDefinition) int {
		return cmp.Or(
			cmp.Compare(a.Name, b.Name),
			cmp.Compare(a.Namespace, b.Namespace),
		)
	})

	for _, client := range definitions.Items {
		hub.Status.SubResourceCatalog = append(hub.Status.SubResourceCatalog, infrav1beta1.ResourceReference{
			Kind:       client.Kind,
			Name:       client.Name,
			APIVersion: client.APIVersion,
		})
	}

	return hub, definitions.Items, nil
}

func (r *SwaggerHubReconciler) patchStatus(ctx context.Context, hub *infrav1beta1.SwaggerHub) error {
	key := client.ObjectKeyFromObject(hub)
	latest := &infrav1beta1.SwaggerHub{}
	if err := r.Client.Get(ctx, key, latest); err != nil {
		return err
	}

	return r.Client.Status().Patch(ctx, hub, client.MergeFrom(latest))
}

// objectKey returns client.ObjectKey for the object.
func objectKey(object metav1.Object) client.ObjectKey {
	return client.ObjectKey{
		Namespace: object.GetNamespace(),
		Name:      object.GetName(),
	}
}
