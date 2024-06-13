package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/DoodleScheduling/swagger-hub-controller/api/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func needExactStatus(reconciledInstance *v1beta1.SwaggerHub, expectedStatus *v1beta1.SwaggerHubStatus) error {
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

var _ = Describe("SwagggerHub controller", func() {
	const (
		timeout  = time.Second * 4
		interval = time.Millisecond * 200
	)

	var eventuallyMatchExactConditions = func(ctx context.Context, instanceLookupKey types.NamespacedName, reconciledInstance *v1beta1.SwaggerHub, expectedStatus *v1beta1.SwaggerHubStatus) {
		Eventually(func() error {
			err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
			if err != nil {
				return err
			}

			return needExactStatus(reconciledInstance, expectedStatus)
		}, timeout, interval).Should(BeNil())
	}

	When("reconciling a suspended SwagggerHub", func() {
		hubName := fmt.Sprintf("hub-%s", randStringRunes(5))

		It("should not update the status", func() {
			By("creating a new SwaggerHub")
			ctx := context.Background()

			gi := &v1beta1.SwaggerHub{
				ObjectMeta: metav1.ObjectMeta{
					Name:      hubName,
					Namespace: "default",
				},
				Spec: v1beta1.SwaggerHubSpec{
					Suspend: true,
				},
			}
			Expect(k8sClient.Create(ctx, gi)).Should(Succeed())

			By("waiting for the reconciliation")
			instanceLookupKey := types.NamespacedName{Name: hubName, Namespace: "default"}
			reconciledInstance := &v1beta1.SwaggerHub{}

			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, &v1beta1.SwaggerHubStatus{})
		})
	})

	When("it reconciles a hub without spec definitions", func() {
		hubName := fmt.Sprintf("hub-%s", randStringRunes(5))
		var hub *v1beta1.SwaggerHub

		It("creates a new hub", func() {
			ctx := context.Background()

			hub = &v1beta1.SwaggerHub{
				ObjectMeta: metav1.ObjectMeta{
					Name:      hubName,
					Namespace: "default",
				},
				Spec: v1beta1.SwaggerHubSpec{},
			}
			Expect(k8sClient.Create(ctx, hub)).Should(Succeed())
		})

		It("should create a service", func() {
			instanceLookupKey := types.NamespacedName{Name: fmt.Sprintf("swagger-ui-%s", hubName), Namespace: "default"}
			reconciledInstance := &corev1.Service{}

			Eventually(func() error {
				return k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
			}, timeout, interval).Should(BeNil())

			Expect(reconciledInstance.Spec.Selector).To(Equal(map[string]string{
				"app.kubernetes.io/instance": "swagger-ui",
				"app.kubernetes.io/name":     "swagger-ui",
				"swagger-hub-controller/hub": hubName,
			}))
			Expect(reconciledInstance.OwnerReferences[0].Name).Should(Equal(hubName))
		})

		It("should create a deployment", func() {
			instanceLookupKey := types.NamespacedName{Name: fmt.Sprintf("swagger-ui-%s", hubName), Namespace: "default"}
			reconciledInstance := &appsv1.Deployment{}

			Eventually(func() error {
				return k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
			}, timeout, interval).Should(BeNil())

			Expect(reconciledInstance.Spec.Template.ObjectMeta.Labels).To(Equal(map[string]string{
				"app.kubernetes.io/instance": "swagger-ui",
				"app.kubernetes.io/name":     "swagger-ui",
				"swagger-hub-controller/hub": hubName,
			}))

			Expect(reconciledInstance.ObjectMeta.Labels).To(Equal(map[string]string{
				"app.kubernetes.io/instance": "swagger-ui",
				"app.kubernetes.io/name":     "swagger-ui",
				"swagger-hub-controller/hub": hubName,
			}))

			Expect(reconciledInstance.Spec.Template.Spec.Containers[0].Name).To(Equal("swagger-ui"))
			Expect(reconciledInstance.Spec.Template.Spec.Containers[0].Image).To(Equal("swaggerapi/swagger-ui:latest"))
			Expect(len(reconciledInstance.Spec.Template.Spec.Containers[0].Env)).To(Equal(0))
			Expect(reconciledInstance.OwnerReferences[0].Name).Should(Equal(hubName))
		})

		It("should update the hub status", func() {
			ctx := context.Background()
			instanceLookupKey := types.NamespacedName{Name: hubName, Namespace: "default"}
			reconciledInstance := &v1beta1.SwaggerHub{}

			expectedStatus := &v1beta1.SwaggerHubStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionTrue,
						Reason:  "ReconciliationSuccessful",
						Message: fmt.Sprintf("deployment/swagger-ui-%s created", hubName),
					},
				},
			}
			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, expectedStatus)
			Expect(len(reconciledInstance.Status.SubResourceCatalog)).Should(Equal(0))
		})

		It("cleans up", func() {
			ctx := context.Background()
			Expect(k8sClient.Delete(ctx, hub)).Should(Succeed())
		})
	})

	When("it reconciles a hub with spec definitions", func() {
		hubName := fmt.Sprintf("hub-%s", randStringRunes(5))
		spec1Name := fmt.Sprintf("spec-%s", randStringRunes(5))
		spec2Name := fmt.Sprintf("spec-%s", randStringRunes(5))
		var hub *v1beta1.SwaggerHub
		var spec1 *v1beta1.SwaggerDefinition
		var spec2 *v1beta1.SwaggerDefinition

		It("creates a new hub", func() {
			ctx := context.Background()

			hub = &v1beta1.SwaggerHub{
				ObjectMeta: metav1.ObjectMeta{
					Name:      hubName,
					Namespace: "default",
				},
				Spec: v1beta1.SwaggerHubSpec{
					DefinitionSelector: &metav1.LabelSelector{},
				},
			}
			Expect(k8sClient.Create(ctx, hub)).Should(Succeed())
		})

		It("creates definitions", func() {
			ctx := context.Background()

			u1 := "https://spec-url-1"
			u2 := "https://spec-url-2"
			spec1 = &v1beta1.SwaggerDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      spec1Name,
					Namespace: "default",
				},
				Spec: v1beta1.SwaggerDefinitionSpec{
					URL: &u1,
				},
			}
			spec2 = &v1beta1.SwaggerDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      spec2Name,
					Namespace: "default",
				},
				Spec: v1beta1.SwaggerDefinitionSpec{
					URL: &u2,
				},
			}

			Expect(k8sClient.Create(ctx, spec1)).Should(Succeed())
			Expect(k8sClient.Create(ctx, spec2)).Should(Succeed())
		})

		It("should update the hub status", func() {
			ctx := context.Background()
			instanceLookupKey := types.NamespacedName{Name: hubName, Namespace: "default"}
			reconciledInstance := &v1beta1.SwaggerHub{}

			expectedStatus := &v1beta1.SwaggerHubStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionTrue,
						Reason:  "ReconciliationSuccessful",
						Message: fmt.Sprintf("deployment/swagger-ui-%s created", hubName),
					},
				},
			}
			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, expectedStatus)
			Expect(reconciledInstance.Status.SubResourceCatalog).Should(Equal([]v1beta1.ResourceReference{
				{
					Kind:       "SwaggerDefinition",
					Name:       spec1Name,
					APIVersion: "swagger.infra.doodle.com/v1beta1",
				},
				{
					Kind:       "SwaggerDefinition",
					Name:       spec2Name,
					APIVersion: "swagger.infra.doodle.com/v1beta1",
				},
			}))
		})

		It("should create a service", func() {
			instanceLookupKey := types.NamespacedName{Name: fmt.Sprintf("swagger-ui-%s", hubName), Namespace: "default"}
			reconciledInstance := &corev1.Service{}

			Eventually(func() error {
				return k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
			}, timeout, interval).Should(BeNil())

			Expect(reconciledInstance.Spec.Selector).To(Equal(map[string]string{
				"app.kubernetes.io/instance": "swagger-ui",
				"app.kubernetes.io/name":     "swagger-ui",
				"swagger-hub-controller/hub": hubName,
			}))
			Expect(reconciledInstance.OwnerReferences[0].Name).Should(Equal(hubName))
		})

		It("should create a deployment", func() {
			instanceLookupKey := types.NamespacedName{Name: fmt.Sprintf("swagger-ui-%s", hubName), Namespace: "default"}
			reconciledInstance := &appsv1.Deployment{}

			Eventually(func() error {
				return k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
			}, timeout, interval).Should(BeNil())

			Expect(reconciledInstance.Spec.Template.ObjectMeta.Labels).To(Equal(map[string]string{
				"app.kubernetes.io/instance": "swagger-ui",
				"app.kubernetes.io/name":     "swagger-ui",
				"swagger-hub-controller/hub": hubName,
			}))

			Expect(reconciledInstance.ObjectMeta.Labels).To(Equal(map[string]string{
				"app.kubernetes.io/instance": "swagger-ui",
				"app.kubernetes.io/name":     "swagger-ui",
				"swagger-hub-controller/hub": hubName,
			}))

			Expect(reconciledInstance.Spec.Template.Spec.Containers[0].Name).To(Equal("swagger-ui"))
			Expect(reconciledInstance.Spec.Template.Spec.Containers[0].Image).To(Equal("swaggerapi/swagger-ui:latest"))
			Expect(reconciledInstance.Spec.Template.Spec.Containers[0].Env).To(Equal([]corev1.EnvVar{
				{
					Name:  "API_URLS",
					Value: fmt.Sprintf(`[{"name":"%s:default","url":"https://spec-url-1"},{"name":"%s:default","url":"https://spec-url-2"}]`, spec1Name, spec2Name),
				},
			}))
			Expect(reconciledInstance.OwnerReferences[0].Name).Should(Equal(hubName))
		})

		It("cleans up", func() {
			ctx := context.Background()
			Expect(k8sClient.Delete(ctx, hub)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, spec1)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, spec2)).Should(Succeed())
		})
	})

	When("it reconciles a hub with a custom template", func() {
		hubName := fmt.Sprintf("hub-%s", randStringRunes(5))
		var hub *v1beta1.SwaggerHub
		var replicas int32 = 3

		It("creates a new hub", func() {
			ctx := context.Background()

			hub = &v1beta1.SwaggerHub{
				ObjectMeta: metav1.ObjectMeta{
					Name:      hubName,
					Namespace: "default",
				},
				Spec: v1beta1.SwaggerHubSpec{
					DeploymentTemplate: &v1beta1.DeploymentTemplate{
						Spec: v1beta1.DeploymentSpec{
							Replicas: &replicas,
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											Name: "swagger-ui",
											Env: []corev1.EnvVar{
												{
													Name:  "FOO",
													Value: "bar",
												},
											},
										},
									},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, hub)).Should(Succeed())
		})

		It("should update the hub status", func() {
			ctx := context.Background()
			instanceLookupKey := types.NamespacedName{Name: hubName, Namespace: "default"}
			reconciledInstance := &v1beta1.SwaggerHub{}

			expectedStatus := &v1beta1.SwaggerHubStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionTrue,
						Reason:  "ReconciliationSuccessful",
						Message: fmt.Sprintf("deployment/swagger-ui-%s created", hubName),
					},
				},
			}
			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, expectedStatus)
			Expect(len(reconciledInstance.Status.SubResourceCatalog)).Should(Equal(0))
		})

		It("should create a deployment", func() {
			instanceLookupKey := types.NamespacedName{Name: fmt.Sprintf("swagger-ui-%s", hubName), Namespace: "default"}
			reconciledInstance := &appsv1.Deployment{}

			Eventually(func() error {
				return k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
			}, timeout, interval).Should(BeNil())

			Expect(reconciledInstance.Spec.Template.ObjectMeta.Labels).To(Equal(map[string]string{
				"app.kubernetes.io/instance": "swagger-ui",
				"app.kubernetes.io/name":     "swagger-ui",
				"swagger-hub-controller/hub": hubName,
			}))

			Expect(reconciledInstance.ObjectMeta.Labels).To(Equal(map[string]string{
				"app.kubernetes.io/instance": "swagger-ui",
				"app.kubernetes.io/name":     "swagger-ui",
				"swagger-hub-controller/hub": hubName,
			}))

			Expect(reconciledInstance.Spec.Template.Spec.Containers[0].Name).To(Equal("swagger-ui"))
			Expect(reconciledInstance.Spec.Template.Spec.Containers[0].Image).To(Equal("swaggerapi/swagger-ui:latest"))
			Expect(reconciledInstance.Spec.Template.Spec.Containers[0].Env).To(Equal([]corev1.EnvVar{
				{
					Name:  "FOO",
					Value: "bar",
				},
			}))
			Expect(reconciledInstance.OwnerReferences[0].Name).Should(Equal(hubName))
		})

		It("cleans up", func() {
			ctx := context.Background()
			Expect(k8sClient.Delete(ctx, hub)).Should(Succeed())
		})
	})
})
