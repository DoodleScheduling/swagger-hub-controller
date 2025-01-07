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
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1beta1 "github.com/DoodleScheduling/swagger-hub-controller/api/v1beta1"
)

// SwaggerSpecification reconciles a SwaggerSpecification object
type SwaggerSpecificationReconciler struct {
	client.Client
	Log        logr.Logger
	Scheme     *runtime.Scheme
	Recorder   record.EventRecorder
	HTTPClient httpClient
}

type httpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type SwaggerSpecificationReconcilerOptions struct {
	MaxConcurrentReconciles int
}

// SetupWithManager adding controllers
func (r *SwaggerSpecificationReconciler) SetupWithManager(mgr ctrl.Manager, opts SwaggerSpecificationReconcilerOptions) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1beta1.SwaggerSpecification{}, builder.WithPredicates(
			predicate.GenerationChangedPredicate{},
		)).
		Watches(
			&infrav1beta1.SwaggerDefinition{},
			handler.EnqueueRequestsFromMapFunc(r.requestsForChangeBySelector),
		).
		WithOptions(controller.Options{MaxConcurrentReconciles: opts.MaxConcurrentReconciles}).
		Complete(r)
}

func (r *SwaggerSpecificationReconciler) requestsForChangeBySelector(ctx context.Context, o client.Object) []reconcile.Request {
	var list infrav1beta1.SwaggerSpecificationList
	if err := r.List(ctx, &list, client.InNamespace(o.GetNamespace())); err != nil {
		return nil
	}

	var reqs []reconcile.Request
	for _, specification := range list.Items {
		labelSel, err := metav1.LabelSelectorAsSelector(specification.Spec.DefinitionSelector)
		if err != nil {
			r.Log.Error(err, "can not select resourceSelector selectors")
			continue
		}

		if labelSel.Matches(labels.Set(o.GetLabels())) {
			r.Log.V(1).Info("referenced resource from a SwaggerSpecification changed detected", "namespace", specification.GetNamespace(), "specification-name", specification.GetName())
			reqs = append(reqs, reconcile.Request{NamespacedName: objectKey(&specification)})
		}
	}

	return reqs
}

// Reconcile SwaggerSpecifications
func (r *SwaggerSpecificationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("namespace", req.Namespace, "name", req.NamespacedName)
	logger.Info("reconciling SwaggerSpecification")

	// Fetch the SwaggerSpecification instance
	specification := infrav1beta1.SwaggerSpecification{}

	err := r.Client.Get(ctx, req.NamespacedName, &specification)
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

	if specification.Spec.Suspend {
		return ctrl.Result{}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, time.Second*3)
	defer cancel()

	specification, result, err := r.reconcile(ctx, specification)
	specification.Status.ObservedGeneration = specification.GetGeneration()

	if err != nil {
		logger.Error(err, "reconcile error occurred")
		specification = infrav1beta1.SwaggerSpecificationReady(specification, metav1.ConditionFalse, "ReconciliationFailed", err.Error())
		r.Recorder.Event(&specification, "Normal", "error", err.Error())
	}

	// Update status after reconciliation.
	if err := r.patchStatus(ctx, &specification); err != nil {
		logger.Error(err, "unable to update status after reconciliation")
		return ctrl.Result{Requeue: true}, err
	}

	return result, err
}

type fetchResult struct {
	definition *infrav1beta1.SwaggerDefinition
	spec       *openapi3.T
	err        error
	basePath   string
	refID      int
}

func (r *SwaggerSpecificationReconciler) generateOpenAPI(ctx context.Context, specification infrav1beta1.SwaggerSpecification, definitions []infrav1beta1.SwaggerDefinition) (infrav1beta1.SwaggerSpecification, openapi3.T, error) {
	schema := openapi3.T{
		OpenAPI: "3.0.1",
		Info: &openapi3.Info{
			Title:          specification.Spec.Info.Title,
			Description:    specification.Spec.Info.Description,
			TermsOfService: specification.Spec.Info.TermsOfService,
			Contact: &openapi3.Contact{
				Name:  specification.Spec.Info.Contact.Name,
				Email: specification.Spec.Info.Contact.Email,
				URL:   specification.Spec.Info.Contact.URL,
			},
			License: &openapi3.License{
				Name: specification.Spec.Info.License.Name,
				URL:  specification.Spec.Info.License.URL,
			},
		},
		Components: &openapi3.Components{
			Schemas: make(openapi3.Schemas),
		},
		Servers: openapi3.Servers{},
	}

	for _, srv := range specification.Spec.Servers {
		schema.Servers = append(schema.Servers, &openapi3.Server{
			URL:         srv.URL,
			Description: srv.Description,
		})
	}

	results := make(chan fetchResult, len(definitions))

	var wg sync.WaitGroup
	wg.Add(len(definitions))

	for refID, definition := range definitions {
		go func(refID int, definition infrav1beta1.SwaggerDefinition) {
			defer wg.Done()

			loader := openapi3.NewLoader()
			loader.Context = ctx

			untypedSpec := make(map[string]interface{})

			err := r.fetchDefinition(ctx, *definition.Spec.URL, &untypedSpec)
			if err != nil {
				results <- fetchResult{
					definition: &definition,
					err:        fmt.Errorf("failed to load SwaggerDefinition url: %w", err),
					refID:      refID,
				}

				return
			}

			prefixRefs(untypedSpec, definition.Name)

			if v, ok := untypedSpec["components"]; ok {
				if v, ok := v.(map[string]interface{})["schemas"]; ok {
					schemas := make(map[string]interface{})
					for name, definitionSchema := range v.(map[string]interface{}) {
						newName := fmt.Sprintf("%s.%s", definition.Name, name)
						schemas[newName] = definitionSchema
					}

					untypedSpec["components"].(map[string]interface{})["schemas"] = schemas
				}
			}

			b, err := json.Marshal(untypedSpec)
			if err != nil {
				results <- fetchResult{
					definition: &definition,
					err:        fmt.Errorf("failed to marshal specification: %w", err),
					refID:      refID,
				}

				return
			}

			s, err := loader.LoadFromData(b)
			if err != nil {
				results <- fetchResult{
					definition: &definition,
					err:        fmt.Errorf("failed to load SwaggerDefinition url: %w", err),
					refID:      refID,
				}

				return
			}

			basePath, err := s.Servers.BasePath()
			if err != nil {
				err = fmt.Errorf("failed to extract base path: %w", err)
			}

			results <- fetchResult{
				definition: &definition,
				spec:       s,
				basePath:   basePath,
				err:        err,
				refID:      refID,
			}
		}(refID, definition)
	}

	wg.Wait()
	close(results)

	var paths []openapi3.NewPathsOption
	var components = make(openapi3.Schemas)

	for result := range results {
		if result.err != nil {
			specification.Status.SubResourceCatalog[result.refID].Error = result.err.Error()
			continue
		}

		if result.spec.Components != nil {
			for name, schema := range result.spec.Components.Schemas {
				components[name] = schema
			}
		}

		for _, pathItem := range result.spec.Paths.Map() {
			for _, op := range pathItem.Operations() {
				op.Tags = []string{result.definition.Name}
			}
		}

		for path, pathItem := range result.spec.Paths.Map() {
			basePath := result.basePath
			if basePath == "/" {
				basePath = ""
			}

			paths = append(paths, openapi3.WithPath(fmt.Sprintf("%s%s", basePath, path), pathItem))
		}
	}

	schema.Paths = openapi3.NewPaths(paths...)
	schema.Components = &openapi3.Components{
		Schemas: components,
	}

	return specification, schema, nil
}

func (r *SwaggerSpecificationReconciler) fetchDefinition(ctx context.Context, url string, to interface{}) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create request failed: %w", err)
	}
	req = req.WithContext(ctx)

	res, err := r.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("send request failed: %w", err)
	}

	if res.Body != nil {
		defer res.Body.Close()
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("read response body failed: %w", err)
	}

	err = json.Unmarshal(body, to)
	if err != nil {
		return fmt.Errorf("decode response body failed: %w", err)
	}

	return nil
}

func prefixRefs(v interface{}, name string) {
	switch v := v.(type) {
	case []interface{}:
		for _, vv := range v {
			prefixRefs(vv, name)
		}
	case map[string]interface{}:
		for k, vv := range v {
			if k == "$ref" {
				v[k] = prefixRef(vv.(string), name)
			} else {
				prefixRefs(vv, name)
			}
		}
	}
}

func prefixRef(ref string, prefix string) string {
	if !strings.HasPrefix(ref, "#") {
		return ref
	}

	newRef := replaceLast(ref, "/", fmt.Sprintf("/%s.", prefix))
	if newRef != ref {
		return newRef
	}

	return ref
}

func replaceLast(x, y, z string) (x2 string) {
	i := strings.LastIndex(x, y)
	if i == -1 {
		return x
	}
	return x[:i] + z + x[i+len(y):]
}

func (r *SwaggerSpecificationReconciler) reconcile(ctx context.Context, specification infrav1beta1.SwaggerSpecification) (infrav1beta1.SwaggerSpecification, ctrl.Result, error) {
	specification.Status.SubResourceCatalog = []infrav1beta1.ResourceLookupReference{}

	specification, definitions, err := r.extendSpecificationWithDefinitions(ctx, specification)
	if err != nil {
		return specification, ctrl.Result{}, err
	}

	specification, openapiSpec, err := r.generateOpenAPI(ctx, specification, definitions)
	if err != nil {
		return specification, ctrl.Result{}, err
	}

	controllerOwner := true
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("swagger-specification-%s", specification.Name),
			Namespace: specification.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					Name:       specification.Name,
					APIVersion: specification.APIVersion,
					Kind:       specification.Kind,
					UID:        specification.UID,
					Controller: &controllerOwner,
				},
			},
		},
	}

	specJSON, err := openapiSpec.MarshalJSON()
	if err != nil {
		return specification, ctrl.Result{}, err
	}

	cm.BinaryData = make(map[string][]byte)
	cm.BinaryData["specification.json"] = specJSON

	var existingSpec corev1.ConfigMap
	err = r.Client.Get(ctx, client.ObjectKey{
		Namespace: cm.Namespace,
		Name:      cm.Name,
	}, &existingSpec)

	if err != nil && !apierrors.IsNotFound(err) {
		return specification, ctrl.Result{}, err
	}

	if apierrors.IsNotFound(err) {
		if err := r.Client.Create(ctx, cm); err != nil {
			return specification, ctrl.Result{}, err
		}
	} else {
		if err := r.Client.Update(ctx, cm); err != nil {
			return specification, ctrl.Result{}, err
		}
	}

	specification = infrav1beta1.SwaggerSpecificationReady(specification, metav1.ConditionTrue, "ReconciliationSuccessful", fmt.Sprintf("configmap/%s created", cm.Name))
	return specification, ctrl.Result{}, nil
}

func (r *SwaggerSpecificationReconciler) extendSpecificationWithDefinitions(ctx context.Context, specification infrav1beta1.SwaggerSpecification) (infrav1beta1.SwaggerSpecification, []infrav1beta1.SwaggerDefinition, error) {
	var definitions infrav1beta1.SwaggerDefinitionList
	definitionSelector, err := metav1.LabelSelectorAsSelector(specification.Spec.DefinitionSelector)
	if err != nil {
		return specification, nil, err
	}

	var namespaces corev1.NamespaceList
	if specification.Spec.NamespaceSelector == nil {
		namespaces.Items = append(namespaces.Items, corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: specification.Namespace,
			},
		})
	} else {
		namespaceSelector, err := metav1.LabelSelectorAsSelector(specification.Spec.NamespaceSelector)
		if err != nil {
			return specification, nil, err
		}

		err = r.Client.List(ctx, &namespaces, client.MatchingLabelsSelector{Selector: namespaceSelector})
		if err != nil {
			return specification, nil, err
		}
	}

	for _, namespace := range namespaces.Items {
		var namespacedDefinitions infrav1beta1.SwaggerDefinitionList
		err = r.Client.List(ctx, &namespacedDefinitions, client.InNamespace(namespace.Name), client.MatchingLabelsSelector{Selector: definitionSelector})
		if err != nil {
			return specification, nil, err
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
		specification.Status.SubResourceCatalog = append(specification.Status.SubResourceCatalog, infrav1beta1.ResourceLookupReference{
			Kind:       client.Kind,
			Name:       client.Name,
			APIVersion: client.APIVersion,
		})
	}

	return specification, definitions.Items, nil
}

func (r *SwaggerSpecificationReconciler) patchStatus(ctx context.Context, specification *infrav1beta1.SwaggerSpecification) error {
	key := client.ObjectKeyFromObject(specification)
	latest := &infrav1beta1.SwaggerSpecification{}
	if err := r.Client.Get(ctx, key, latest); err != nil {
		return err
	}

	return r.Client.Status().Patch(ctx, specification, client.MergeFrom(latest))
}
