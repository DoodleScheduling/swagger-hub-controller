package controllers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/DoodleScheduling/swagger-hub-controller/api/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func needExactDefinitionStatus(reconciledInstance *v1beta1.SwaggerDefinition, expectedStatus *v1beta1.SwaggerDefinitionStatus) error {
	var expectedConditions []string
	var currentConditions []string

	for _, expectedCondition := range expectedStatus.Conditions {
		expectedConditions = append(expectedConditions, expectedCondition.Type)
		var hasCondition bool
		for _, condition := range reconciledInstance.Status.Conditions {
			if expectedCondition.Type == condition.Type {
				hasCondition = true

				if expectedCondition.Status != condition.Status {
					return fmt.Errorf("condition %s does not match expected status %s, current status=%s; current conditions=%#v", expectedCondition.Type, expectedCondition.Status, condition.Status, reconciledInstance.Status.Conditions)
				}
				if expectedCondition.Reason != condition.Reason {
					return fmt.Errorf("condition %s does not match expected reason %s, current reason=%s; current conditions=%#v", expectedCondition.Type, expectedCondition.Reason, condition.Reason, reconciledInstance.Status.Conditions)
				}
				if expectedCondition.Message != condition.Message {
					return fmt.Errorf("condition %s does not match expected message %s, current status=%s; current conditions=%#v", expectedCondition.Type, expectedCondition.Message, condition.Message, reconciledInstance.Status.Conditions)
				}
			}
		}

		if !hasCondition {
			return fmt.Errorf("missing condition %s", expectedCondition.Type)
		}
	}

	for _, condition := range reconciledInstance.Status.Conditions {
		currentConditions = append(currentConditions, condition.Type)
	}

	if len(expectedConditions) != len(currentConditions) {
		return fmt.Errorf("expected conditions %#v do not match, current conditions=%#v", expectedConditions, currentConditions)
	}

	return nil
}

var _ = Describe("SwaggerDefinition controller", func() {
	const (
		timeout  = time.Second * 4
		interval = time.Millisecond * 200
	)

	var eventuallyMatchExactConditions = func(ctx context.Context, instanceLookupKey types.NamespacedName, reconciledInstance *v1beta1.SwaggerDefinition, expectedStatus *v1beta1.SwaggerDefinitionStatus) {
		Eventually(func() error {
			err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
			if err != nil {
				return err
			}

			return needExactDefinitionStatus(reconciledInstance, expectedStatus)
		}, timeout, interval).Should(BeNil())
	}

	When("reconciling a suspended SwaggerDefinition", func() {
		definitionName := fmt.Sprintf("definition-%s", randStringRunes(5))

		It("should not update the status", func() {
			By("creating a new SwaggerDefinition")
			ctx := context.Background()

			gi := &v1beta1.SwaggerDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      definitionName,
					Namespace: "default",
				},
				Spec: v1beta1.SwaggerDefinitionSpec{
					Suspend: true,
				},
			}
			Expect(k8sClient.Create(ctx, gi)).Should(Succeed())

			By("waiting for the reconciliation")
			instanceLookupKey := types.NamespacedName{Name: definitionName, Namespace: "default"}
			reconciledInstance := &v1beta1.SwaggerDefinition{}

			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, &v1beta1.SwaggerDefinitionStatus{})
		})
	})

	When("it reconciles a definition with a http spec source", func() {
		definitionName := fmt.Sprintf("definition-%s", randStringRunes(5))
		var definition *v1beta1.SwaggerDefinition
		url := "http://api"

		It("creates a new definition", func() {
			ctx := context.Background()

			testHttpClient.MockResponse(mockHttpRequest{url: url, verb: http.MethodGet}, &mockHttpResponse{
				r:   &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(`{"components":{},"info":{"contact":{},"license":{"name":""},"title":"foo","version":""},"openapi":"3.0.1","paths":{},"servers":[{"url":"http://api"}]}`))},
				err: nil,
			})

			definition = &v1beta1.SwaggerDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      definitionName,
					Namespace: "default",
				},
				Spec: v1beta1.SwaggerDefinitionSpec{
					URL: &url,
				},
			}
			Expect(k8sClient.Create(ctx, definition)).Should(Succeed())
		})

		It("should create a new swagger definition configmap", func() {
			ctx := context.Background()
			key := types.NamespacedName{Name: fmt.Sprintf("swagger-definition-%s", definitionName), Namespace: "default"}
			cm := &corev1.ConfigMap{}

			Eventually(func() error {
				return k8sClient.Get(ctx, key, cm)
			}, timeout, interval).Should(BeNil())

			expected := `{"components":{},"info":{"contact":{},"license":{"name":""},"title":"foo","version":""},"openapi":"3.0.1","paths":{},"servers":[{"url":"http://api"}]}`
			Expect(string(cm.BinaryData["definition.json"])).Should(Equal(expected))
		})

		It("should update the definition status", func() {
			ctx := context.Background()
			instanceLookupKey := types.NamespacedName{Name: definitionName, Namespace: "default"}
			reconciledInstance := &v1beta1.SwaggerDefinition{}

			expectedStatus := &v1beta1.SwaggerDefinitionStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionTrue,
						Reason:  "ReconciliationSuccessful",
						Message: fmt.Sprintf("configmap/swagger-definition-%s created", definitionName),
					},
				},
			}
			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, expectedStatus)
		})

		It("cleans up", func() {
			ctx := context.Background()
			Expect(k8sClient.Delete(ctx, definition)).Should(Succeed())
		})
	})

	When("a definition requires basic auth", func() {
		var (
			definition *v1beta1.SwaggerDefinition
			secret     *corev1.Secret
			server     *httptest.Server
			name       = fmt.Sprintf("basicauth-%s", randStringRunes(5))
		)

		It("creates a new definition", func() {
			ctx := context.Background()

			By("serving a definition behind basic auth over https")
			server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				username, password, ok := r.BasicAuth()
				if !ok || username != "user" || password != "pass" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}

				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"openapi":"3.0.1","info":{"title":"secured","version":"1"},"paths":{"/secured":{"get":{"responses":{"200":{"description":"ok"}}}}}}`))
			}))

			testHTTPClient.set(server.Client())

			secret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: "default",
				},
				Data: map[string][]byte{
					"username": []byte("user"),
					"password": []byte("pass"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			definition = &v1beta1.SwaggerDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: "default",
				},
				Spec: v1beta1.SwaggerDefinitionSpec{
					URL: &server.URL,
					Auth: &v1beta1.DefinitionAuth{
						Basic: &v1beta1.BasicAuth{
							SecretRef: v1beta1.LocalSecretReference{
								Name: name,
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, definition)).Should(Succeed())
		})

		It("should create a new swagger definition configmap using the credentials from the referenced secret", func() {
			ctx := context.Background()
			key := types.NamespacedName{Name: fmt.Sprintf("swagger-definition-%s", name), Namespace: "default"}
			cm := &corev1.ConfigMap{}

			Eventually(func() string {
				if err := k8sClient.Get(ctx, key, cm); err != nil {
					return ""
				}

				return string(cm.BinaryData["definition.json"])
			}, timeout, interval).Should(ContainSubstring(`"/secured"`))
		})

		It("should update the definition status", func() {
			ctx := context.Background()
			instanceLookupKey := types.NamespacedName{Name: name, Namespace: "default"}
			reconciledInstance := &v1beta1.SwaggerDefinition{}

			expectedStatus := &v1beta1.SwaggerDefinitionStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionTrue,
						Reason:  "ReconciliationSuccessful",
						Message: fmt.Sprintf("configmap/swagger-definition-%s created", name),
					},
				},
			}
			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, expectedStatus)
		})

		It("cleans up", func() {
			ctx := context.Background()
			server.Close()
			testHTTPClient.set(http.DefaultClient)
			Expect(k8sClient.Delete(ctx, definition)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, secret)).Should(Succeed())
		})
	})

	When("a definition with basic auth points to an insecure url", func() {
		var (
			definition *v1beta1.SwaggerDefinition
			name       = fmt.Sprintf("insecure-%s", randStringRunes(5))
			url        = "http://insecure/openapi"
		)

		It("creates a new definition", func() {
			ctx := context.Background()

			definition = &v1beta1.SwaggerDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: "default",
				},
				Spec: v1beta1.SwaggerDefinitionSpec{
					URL: &url,
					Auth: &v1beta1.DefinitionAuth{
						Basic: &v1beta1.BasicAuth{
							SecretRef: v1beta1.LocalSecretReference{
								Name: name,
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, definition)).Should(Succeed())
		})

		It("should not create a swagger definition configmap", func() {
			ctx := context.Background()
			key := types.NamespacedName{Name: fmt.Sprintf("swagger-definition-%s", name), Namespace: "default"}
			cm := &corev1.ConfigMap{}

			Consistently(func() bool {
				return apierrors.IsNotFound(k8sClient.Get(ctx, key, cm))
			}, time.Second, interval).Should(BeTrue())
		})

		It("should report the refused credentials in the definition status", func() {
			ctx := context.Background()
			instanceLookupKey := types.NamespacedName{Name: name, Namespace: "default"}
			reconciledInstance := &v1beta1.SwaggerDefinition{}

			Eventually(func() string {
				if err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance); err != nil {
					return ""
				}

				for _, condition := range reconciledInstance.Status.Conditions {
					if condition.Type == v1beta1.ConditionReady {
						return condition.Message
					}
				}

				return ""
			}, timeout, interval).Should(ContainSubstring("refusing to send basic auth credentials to an insecure http:// url"))
		})

		It("cleans up", func() {
			ctx := context.Background()
			Expect(k8sClient.Delete(ctx, definition)).Should(Succeed())
		})
	})

	When("a definition with basic auth allows an insecure url", func() {
		var (
			definition *v1beta1.SwaggerDefinition
			secret     *corev1.Secret
			server     *httptest.Server
			name       = fmt.Sprintf("allowinsecure-%s", randStringRunes(5))
		)

		It("creates a new definition", func() {
			ctx := context.Background()

			By("serving a definition behind basic auth over http")
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				username, password, ok := r.BasicAuth()
				if !ok || username != "user" || password != "pass" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}

				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"openapi":"3.0.1","info":{"title":"insecure","version":"1"},"paths":{"/insecure":{"get":{"responses":{"200":{"description":"ok"}}}}}}`))
			}))

			secret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: "default",
				},
				Data: map[string][]byte{
					"username": []byte("user"),
					"password": []byte("pass"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			definition = &v1beta1.SwaggerDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: "default",
				},
				Spec: v1beta1.SwaggerDefinitionSpec{
					URL: &server.URL,
					Auth: &v1beta1.DefinitionAuth{
						Basic: &v1beta1.BasicAuth{
							AllowInsecure: true,
							SecretRef: v1beta1.LocalSecretReference{
								Name: name,
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, definition)).Should(Succeed())
		})

		It("should create a new swagger definition configmap sending the credentials over http", func() {
			ctx := context.Background()
			key := types.NamespacedName{Name: fmt.Sprintf("swagger-definition-%s", name), Namespace: "default"}
			cm := &corev1.ConfigMap{}

			Eventually(func() string {
				if err := k8sClient.Get(ctx, key, cm); err != nil {
					return ""
				}

				return string(cm.BinaryData["definition.json"])
			}, timeout, interval).Should(ContainSubstring(`"/insecure"`))
		})

		It("cleans up", func() {
			ctx := context.Background()
			server.Close()
			Expect(k8sClient.Delete(ctx, definition)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, secret)).Should(Succeed())
		})
	})

	When("a definition with basic auth configures a static username", func() {
		var (
			definition *v1beta1.SwaggerDefinition
			secret     *corev1.Secret
			server     *httptest.Server
			name       = fmt.Sprintf("staticuser-%s", randStringRunes(5))
		)

		It("creates a new definition", func() {
			ctx := context.Background()

			By("serving a definition behind basic auth over https")
			server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				username, password, ok := r.BasicAuth()
				if !ok || username != "actuator" || password != "pass" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}

				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"openapi":"3.0.1","info":{"title":"static","version":"1"},"paths":{"/static":{"get":{"responses":{"200":{"description":"ok"}}}}}}`))
			}))

			testHTTPClient.set(server.Client())

			By("creating a secret which only holds the password")
			secret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: "default",
				},
				Data: map[string][]byte{
					"password": []byte("pass"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			definition = &v1beta1.SwaggerDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: "default",
				},
				Spec: v1beta1.SwaggerDefinitionSpec{
					URL: &server.URL,
					Auth: &v1beta1.DefinitionAuth{
						Basic: &v1beta1.BasicAuth{
							Username: "actuator",
							SecretRef: v1beta1.LocalSecretReference{
								Name: name,
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, definition)).Should(Succeed())
		})

		It("should create a new swagger definition configmap using the static username", func() {
			ctx := context.Background()
			key := types.NamespacedName{Name: fmt.Sprintf("swagger-definition-%s", name), Namespace: "default"}
			cm := &corev1.ConfigMap{}

			Eventually(func() string {
				if err := k8sClient.Get(ctx, key, cm); err != nil {
					return ""
				}

				return string(cm.BinaryData["definition.json"])
			}, timeout, interval).Should(ContainSubstring(`"/static"`))
		})

		It("cleans up", func() {
			ctx := context.Background()
			server.Close()
			testHTTPClient.set(http.DefaultClient)
			Expect(k8sClient.Delete(ctx, definition)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, secret)).Should(Succeed())
		})
	})
})
