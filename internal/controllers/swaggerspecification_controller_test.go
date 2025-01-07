package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/DoodleScheduling/swagger-hub-controller/api/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func needExactSpecificationStatus(reconciledInstance *v1beta1.SwaggerSpecification, expectedStatus *v1beta1.SwaggerSpecificationStatus) error {
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

var _ = Describe("SwaggerSpecification controller", func() {
	const (
		timeout  = time.Second * 4
		interval = time.Millisecond * 200
	)

	var eventuallyMatchExactConditions = func(ctx context.Context, instanceLookupKey types.NamespacedName, reconciledInstance *v1beta1.SwaggerSpecification, expectedStatus *v1beta1.SwaggerSpecificationStatus) {
		Eventually(func() error {
			err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
			if err != nil {
				return err
			}

			return needExactSpecificationStatus(reconciledInstance, expectedStatus)
		}, timeout, interval).Should(BeNil())
	}

	When("reconciling a suspended SwaggerSpecification", func() {
		specificationName := fmt.Sprintf("specification-%s", randStringRunes(5))

		It("should not update the status", func() {
			By("creating a new SwaggerSpecification")
			ctx := context.Background()

			gi := &v1beta1.SwaggerSpecification{
				ObjectMeta: metav1.ObjectMeta{
					Name:      specificationName,
					Namespace: "default",
				},
				Spec: v1beta1.SwaggerSpecificationSpec{
					Suspend: true,
				},
			}
			Expect(k8sClient.Create(ctx, gi)).Should(Succeed())

			By("waiting for the reconciliation")
			instanceLookupKey := types.NamespacedName{Name: specificationName, Namespace: "default"}
			reconciledInstance := &v1beta1.SwaggerSpecification{}

			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, &v1beta1.SwaggerSpecificationStatus{})
		})
	})

	When("it reconciles a specification without spec definitions", func() {
		specificationName := fmt.Sprintf("specification-%s", randStringRunes(5))
		var specification *v1beta1.SwaggerSpecification

		It("creates a new specification", func() {
			ctx := context.Background()

			specification = &v1beta1.SwaggerSpecification{
				ObjectMeta: metav1.ObjectMeta{
					Name:      specificationName,
					Namespace: "default",
				},
				Spec: v1beta1.SwaggerSpecificationSpec{
					Servers: v1beta1.Servers{
						{
							URL: "http://api",
						},
					},
					Info: v1beta1.Info{
						Title: "foo",
					},
				},
			}
			Expect(k8sClient.Create(ctx, specification)).Should(Succeed())
		})

		It("should create a new swagger specification configmap", func() {
			ctx := context.Background()
			key := types.NamespacedName{Name: fmt.Sprintf("swagger-specification-%s", specificationName), Namespace: "default"}
			cm := &corev1.ConfigMap{}

			Eventually(func() error {
				return k8sClient.Get(ctx, key, cm)
			}, timeout, interval).Should(BeNil())

			expected := `{"components":{},"info":{"contact":{},"license":{"name":""},"title":"foo","version":""},"openapi":"3.0.1","paths":{},"servers":[{"url":"http://api"}]}`
			Expect(string(cm.BinaryData["specification.json"])).Should(Equal(expected))
		})

		It("should update the specification status", func() {
			ctx := context.Background()
			instanceLookupKey := types.NamespacedName{Name: specificationName, Namespace: "default"}
			reconciledInstance := &v1beta1.SwaggerSpecification{}

			expectedStatus := &v1beta1.SwaggerSpecificationStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionTrue,
						Reason:  "ReconciliationSuccessful",
						Message: fmt.Sprintf("configmap/swagger-specification-%s created", specificationName),
					},
				},
			}
			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, expectedStatus)
			Expect(len(reconciledInstance.Status.SubResourceCatalog)).Should(Equal(0))
		})

		It("cleans up", func() {
			ctx := context.Background()
			Expect(k8sClient.Delete(ctx, specification)).Should(Succeed())
		})
	})
})
