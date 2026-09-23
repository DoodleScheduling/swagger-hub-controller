/*
Copyright 2022 Doodle.

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
	"bytes"
	"context"
	"io"
	"math/rand"
	"net/http"
	"path/filepath"
	"sync"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	metricsinfradoodlecomv1beta1 "github.com/DoodleScheduling/swagger-hub-controller/api/v1beta1"
	//+kubebuilder:scaffold:imports
)

// switchableHTTPClient delegates to a http client which can be exchanged by
// each test, this is required to trust the certificate of a test tls server.
type switchableHTTPClient struct {
	mu     sync.Mutex
	client *http.Client
}

func (c *switchableHTTPClient) Do(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	delegate := c.client
	c.mu.Unlock()

	return delegate.Do(req)
}

func (c *switchableHTTPClient) set(client *http.Client) {
	c.mu.Lock()
	c.client = client
	c.mu.Unlock()
}

var testHTTPClient = &switchableHTTPClient{client: http.DefaultClient}

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var k8sClient client.Client
var testEnv *envtest.Environment
var k8sManager ctrl.Manager
var ctx context.Context
var cancel context.CancelFunc
var testHttpClient *mockHttpClient

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "base", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	err = corev1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = metricsinfradoodlecomv1beta1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	k8sManager, err = ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})
	Expect(err).ToNot(HaveOccurred())

	testHttpClient = &mockHttpClient{
		mu: sync.Mutex{},
		r:  make(map[mockHttpRequest]*mockHttpResponse),
	}

	//+kubebuilder:scaffold:scheme
	// SwaggerHub setup
	err = (&SwaggerHubReconciler{
		Client:   k8sManager.GetClient(),
		Log:      ctrl.Log.WithName("controllers").WithName("SwaggerHub"),
		Scheme:   k8sManager.GetScheme(),
		Recorder: k8sManager.GetEventRecorder("SwaggerHub"),
	}).SetupWithManager(k8sManager, SwaggerHubReconcilerOptions{MaxConcurrentReconciles: 10})
	Expect(err).ToNot(HaveOccurred(), "failed to setup SwaggerHub")

	//+kubebuilder:scaffold:scheme
	// SwaggerDefinition setup
	err = (&SwaggerDefinitionReconciler{
		HTTPClient: testHttpClient,
		Client:     k8sManager.GetClient(),
		Log:        ctrl.Log.WithName("controllers").WithName("SwaggerDefinition"),
		Scheme:     k8sManager.GetScheme(),
		Recorder:   k8sManager.GetEventRecorder("SwaggerDefinition"),
	}).SetupWithManager(k8sManager, SwaggerDefinitionReconcilerOptions{MaxConcurrentReconciles: 10})
	Expect(err).ToNot(HaveOccurred(), "failed to setup SwaggerDefinition")

	//+kubebuilder:scaffold:scheme
	// SwaggerSpecification setup
	err = (&SwaggerSpecificationReconciler{
		Client:   k8sManager.GetClient(),
		Log:      ctrl.Log.WithName("controllers").WithName("SwaggerSpecification"),
		Scheme:   k8sManager.GetScheme(),
		Recorder: k8sManager.GetEventRecorder("SwaggerSpecification"),
	}).SetupWithManager(k8sManager, SwaggerSpecificationReconcilerOptions{MaxConcurrentReconciles: 10})
	Expect(err).ToNot(HaveOccurred(), "failed to setup SwaggerSpecification")

	ctx, cancel = context.WithCancel(context.TODO())
	go func() {
		err = k8sManager.Start(ctx)
		Expect(err).ToNot(HaveOccurred())
	}()

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())
})

var _ = AfterSuite(func() {
	cancel()
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).ToNot(HaveOccurred())
})

var letterRunes = []rune("abcdefghijklmnopqrstuvwxyz1234567890")

func randStringRunes(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = letterRunes[rand.Intn(len(letterRunes))]
	}
	return string(b)
}

type mockHttpResponse struct {
	r   *http.Response
	err error

	// body holds the response payload, a reconcile may happen more than once and each attempt
	// needs to read it from the start.
	body []byte
}

type mockHttpClient struct {
	r  map[mockHttpRequest]*mockHttpResponse
	mu sync.Mutex
}

type mockHttpRequest struct {
	url  string
	verb string
}

func (c *mockHttpClient) MockResponse(req mockHttpRequest, res *mockHttpResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if res.r != nil && res.r.Body != nil {
		b, err := io.ReadAll(res.r.Body)
		Expect(err).ToNot(HaveOccurred())
		_ = res.r.Body.Close()

		res.body = b
		res.r.Body = nil
	}

	c.r[req] = res
}

func (c *mockHttpClient) Do(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	mockReq := mockHttpRequest{
		url:  req.URL.String(),
		verb: req.Method,
	}

	if res, ok := c.r[mockReq]; ok {
		if res.r == nil {
			return nil, res.err
		}

		// Hand out a copy, the caller closes the body and the next request needs its own.
		clone := *res.r
		clone.Body = io.NopCloser(bytes.NewReader(res.body))

		return &clone, res.err
	}

	// Tests which serve a definition from a httptest server register no mock, the request is
	// handed to the real client instead which they can exchange to trust a tls test server.
	return testHTTPClient.Do(req)
}
