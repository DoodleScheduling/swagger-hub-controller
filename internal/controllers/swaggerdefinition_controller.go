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
	"fmt"
	"io"
	"net/http"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1beta1 "github.com/DoodleScheduling/swagger-hub-controller/api/v1beta1"
)

// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
// +kubebuilder:rbac:groups=swagger.infra.doodle.com,resources=swaggerdefinitions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=swagger.infra.doodle.com,resources=swaggerdefinitions/status,verbs=get;update;patch

// SwaggerDefinition reconciles a SwaggerDefinition object
type SwaggerDefinitionReconciler struct {
	client.Client
	Log        logr.Logger
	Scheme     *runtime.Scheme
	Recorder   events.EventRecorder
	HTTPClient httpClient
}

type httpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type SwaggerDefinitionReconcilerOptions struct {
	MaxConcurrentReconciles int
}

// SetupWithManager adding controllers
func (r *SwaggerDefinitionReconciler) SetupWithManager(mgr ctrl.Manager, opts SwaggerDefinitionReconcilerOptions) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1beta1.SwaggerDefinition{}, builder.WithPredicates(
			predicate.GenerationChangedPredicate{},
		)).
		WithOptions(controller.Options{MaxConcurrentReconciles: opts.MaxConcurrentReconciles}).
		Complete(r)
}

// Reconcile SwaggerDefinitions
func (r *SwaggerDefinitionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("namespace", req.Namespace, "name", req.NamespacedName)
	logger.Info("reconciling SwaggerDefinition")

	// Fetch the SwaggerDefinition instance
	definition := infrav1beta1.SwaggerDefinition{}

	err := r.Get(ctx, req.NamespacedName, &definition)
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

	if definition.Spec.Suspend {
		return ctrl.Result{}, nil
	}

	if definition.Spec.Timeout.Duration != 0 {
		c, cancel := context.WithTimeout(ctx, definition.Spec.Timeout.Duration)
		ctx = c
		defer cancel()
	}

	definition, result, err := r.reconcile(ctx, definition)
	definition.Status.ObservedGeneration = definition.GetGeneration()

	if err != nil {
		logger.Error(err, "reconcile error occurred")
		definition = infrav1beta1.SwaggerDefinitionReady(definition, metav1.ConditionFalse, "ReconciliationFailed", err.Error())
		r.Recorder.Eventf(&definition, nil, corev1.EventTypeWarning, "Error", "Reconcile", "failed to reconcile: %s", err.Error())
	}

	// Update status after reconciliation.
	if err := r.patchStatus(ctx, &definition); err != nil {
		logger.Error(err, "unable to update status after reconciliation")
		return ctrl.Result{}, err
	}

	if definition.Spec.Interval.Duration > 0 {
		return ctrl.Result{RequeueAfter: definition.Spec.Interval.Duration}, nil
	}

	return result, err
}

func (r *SwaggerDefinitionReconciler) fetchDefinition(ctx context.Context, definition infrav1beta1.SwaggerDefinition) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, *definition.Spec.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request failed: %w", err)
	}
	req = req.WithContext(ctx)

	if err := r.authenticateRequest(ctx, definition, req); err != nil {
		return nil, err
	}

	res, err := r.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request failed: %w", err)
	}

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("request failed with status code %d", res.StatusCode)
	}

	if res.Body != nil {
		defer func() {
			_ = res.Body.Close()
		}()
	}

	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, fmt.Errorf("unexpected http status code %d", res.StatusCode)
	}

	return io.ReadAll(res.Body)
}

// authenticateRequest adds the credentials referenced by the SwaggerDefinition to the request.
func (r *SwaggerDefinitionReconciler) authenticateRequest(ctx context.Context, definition infrav1beta1.SwaggerDefinition, req *http.Request) error {
	if definition.Spec.Auth == nil || definition.Spec.Auth.Basic == nil {
		return nil
	}

	basic := definition.Spec.Auth.Basic
	if req.URL.Scheme != "https" && !basic.AllowInsecure {
		return fmt.Errorf("refusing to send basic auth credentials to an insecure %s:// url", req.URL.Scheme)
	}

	secretRef := basic.SecretRef
	var secret corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: definition.Namespace, Name: secretRef.Name}, &secret); err != nil {
		return fmt.Errorf("failed to get referenced secret %s: %w", secretRef.Name, err)
	}

	usernameField := cmp.Or(secretRef.UsernameField, "username")
	passwordField := cmp.Or(secretRef.PasswordField, "password")

	username := basic.Username
	if username == "" {
		v, ok := secret.Data[usernameField]
		if !ok {
			return fmt.Errorf("field %s not found in secret %s", usernameField, secretRef.Name)
		}

		username = string(v)
	}

	password, ok := secret.Data[passwordField]
	if !ok {
		return fmt.Errorf("field %s not found in secret %s", passwordField, secretRef.Name)
	}

	req.SetBasicAuth(username, string(password))

	return nil
}

func (r *SwaggerDefinitionReconciler) reconcile(ctx context.Context, definition infrav1beta1.SwaggerDefinition) (infrav1beta1.SwaggerDefinition, ctrl.Result, error) {
	controllerOwner := true
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("swagger-definition-%s", definition.Name),
			Namespace: definition.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					Name:       definition.Name,
					APIVersion: definition.APIVersion,
					Kind:       definition.Kind,
					UID:        definition.UID,
					Controller: &controllerOwner,
				},
			},
		},
	}

	if definition.Spec.URL == nil {
		return definition, ctrl.Result{}, fmt.Errorf("url is required")
	}

	b, err := r.fetchDefinition(ctx, definition)
	if err != nil {
		return definition, ctrl.Result{}, fmt.Errorf("failed to fetch definition: %w", err)
	}

	cm.BinaryData = make(map[string][]byte)
	cm.BinaryData["definition.json"] = b

	var existingSpec corev1.ConfigMap
	err = r.Get(ctx, client.ObjectKey{
		Namespace: cm.Namespace,
		Name:      cm.Name,
	}, &existingSpec)

	if err != nil && !apierrors.IsNotFound(err) {
		return definition, ctrl.Result{}, err
	}

	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, cm); err != nil {
			return definition, ctrl.Result{}, err
		}
	} else {
		if err := r.Update(ctx, cm); err != nil {
			return definition, ctrl.Result{}, err
		}
	}

	definition = infrav1beta1.SwaggerDefinitionReady(definition, metav1.ConditionTrue, "ReconciliationSuccessful", fmt.Sprintf("configmap/%s created", cm.Name))
	return definition, ctrl.Result{}, nil
}

func (r *SwaggerDefinitionReconciler) patchStatus(ctx context.Context, definition *infrav1beta1.SwaggerDefinition) error {
	key := client.ObjectKeyFromObject(definition)
	latest := &infrav1beta1.SwaggerDefinition{}
	if err := r.Get(ctx, key, latest); err != nil {
		return err
	}

	return r.Status().Patch(ctx, definition, client.MergeFrom(latest))
}
