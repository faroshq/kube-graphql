//go:build e2e

package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/faroshq/kuery/apis/query/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
)

// --- HTTP helpers ---

func newInsecureClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}

func queryKuery(t *testing.T, spec v1alpha1.QuerySpec) v1alpha1.QueryStatus {
	t.Helper()
	query := map[string]any{
		"apiVersion": "kuery.io/v1alpha1",
		"kind":       "Query",
		"spec":       spec,
	}
	body, err := json.Marshal(query)
	if err != nil {
		t.Fatalf("marshal query: %v", err)
	}

	resp, err := httpClient.Post(serverURL+"/apis/kuery.io/v1alpha1/queries", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST query: %v", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(respBody, &raw); err != nil {
		t.Fatalf("unmarshal response: %v\nbody: %s", err, string(respBody))
	}
	if kind, _ := raw["kind"].(string); kind == "Status" {
		msg, _ := raw["message"].(string)
		t.Fatalf("query failed: %s", msg)
	}

	statusRaw, err := json.Marshal(raw["status"])
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}

	var status v1alpha1.QueryStatus
	if err := json.Unmarshal(statusRaw, &status); err != nil {
		t.Fatalf("unmarshal status: %v\nraw: %s", err, string(statusRaw))
	}
	return status
}

func waitForCondition(timeout, interval time.Duration, fn func() bool) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return nil
		}
		time.Sleep(interval)
	}
	return fmt.Errorf("condition not met within %v", timeout)
}

// --- Result helpers ---

func objectNames(t *testing.T, results []v1alpha1.ObjectResult) []string {
	t.Helper()
	var names []string
	for _, r := range results {
		names = append(names, getProjectedString(t, r.Object, "metadata", "name"))
	}
	return names
}

func objectKinds(t *testing.T, results []v1alpha1.ObjectResult) []string {
	t.Helper()
	var kinds []string
	for _, r := range results {
		kinds = append(kinds, getProjectedString(t, r.Object, "kind"))
	}
	return kinds
}

func getProjectedString(t *testing.T, obj *runtime.RawExtension, path ...string) string {
	t.Helper()
	if obj == nil || len(obj.Raw) == 0 {
		return ""
	}
	var data map[string]any
	if err := json.Unmarshal(obj.Raw, &data); err != nil {
		t.Fatalf("unmarshal projected object: %v", err)
	}
	current := data
	for i, key := range path {
		if i == len(path)-1 {
			val, _ := current[key].(string)
			return val
		}
		next, ok := current[key].(map[string]any)
		if !ok {
			return ""
		}
		current = next
	}
	return ""
}

func getProjectedValue(t *testing.T, obj *runtime.RawExtension, path ...string) any {
	t.Helper()
	if obj == nil || len(obj.Raw) == 0 {
		return nil
	}
	var data map[string]any
	if err := json.Unmarshal(obj.Raw, &data); err != nil {
		return nil
	}
	var current any = data
	for _, key := range path {
		m, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = m[key]
	}
	return current
}

func hasName(t *testing.T, results []v1alpha1.ObjectResult, name string) bool {
	t.Helper()
	return containsString(objectNames(t, results), name)
}

func projectionSpec(spec map[string]any) *runtime.RawExtension {
	raw, _ := json.Marshal(spec)
	return &runtime.RawExtension{Raw: raw}
}

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// --- Kubernetes helpers (replaces kubectl CLI calls) ---

// applyYAMLFromKubeconfig parses multi-document YAML and applies each resource
// to the cluster using the dynamic client. No kubectl needed.
func applyYAMLFromKubeconfig(ctx context.Context, kubeconfig []byte, namespace, yamlContent string) error {
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return fmt.Errorf("parse kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("create dynamic client: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("create clientset: %w", err)
	}

	// Ensure namespace exists.
	_, err = clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: namespace},
	}, metav1.CreateOptions{})
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("create namespace %s: %w", namespace, err)
	}

	// Build REST mapper for GVR resolution.
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return fmt.Errorf("create discovery client: %w", err)
	}
	groupResources, err := restmapper.GetAPIGroupResources(dc)
	if err != nil {
		return fmt.Errorf("get API group resources: %w", err)
	}
	mapper := restmapper.NewDiscoveryRESTMapper(groupResources)

	// Parse multi-document YAML.
	reader := yamlutil.NewYAMLReader(bufio.NewReader(strings.NewReader(yamlContent)))
	for {
		rawDoc, err := reader.Read()
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("read YAML document: %w", err)
		}
		rawDoc = bytes.TrimSpace(rawDoc)
		if len(rawDoc) == 0 || string(rawDoc) == "---" {
			continue
		}

		obj := &unstructured.Unstructured{}
		if err := json.Unmarshal(rawDoc, obj); err != nil {
			// Try YAML→JSON conversion.
			jsonBytes, convErr := yamlutil.ToJSON(rawDoc)
			if convErr != nil {
				return fmt.Errorf("convert YAML to JSON: %w (original: %w)", convErr, err)
			}
			if err := json.Unmarshal(jsonBytes, obj); err != nil {
				return fmt.Errorf("unmarshal JSON: %w", err)
			}
		}

		if obj.GetKind() == "" {
			continue
		}

		// Set namespace if not set.
		if obj.GetNamespace() == "" {
			obj.SetNamespace(namespace)
		}

		// Resolve GVR.
		gvk := obj.GroupVersionKind()
		mapping, err := mapper.RESTMapping(schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind}, gvk.Version)
		if err != nil {
			return fmt.Errorf("resolve GVR for %s: %w", gvk, err)
		}

		var resource dynamic.ResourceInterface
		if mapping.Scope.Name() == "namespace" {
			resource = dynClient.Resource(mapping.Resource).Namespace(obj.GetNamespace())
		} else {
			resource = dynClient.Resource(mapping.Resource)
		}

		// Create or update.
		_, err = resource.Create(ctx, obj, metav1.CreateOptions{})
		if err != nil {
			if strings.Contains(err.Error(), "already exists") {
				_, err = resource.Update(ctx, obj, metav1.UpdateOptions{})
				if err != nil {
					return fmt.Errorf("update %s/%s: %w", gvk.Kind, obj.GetName(), err)
				}
			} else {
				return fmt.Errorf("create %s/%s: %w", gvk.Kind, obj.GetName(), err)
			}
		}
	}
	return nil
}

// waitForDeploymentReady polls until a deployment has all replicas available.
func waitForDeploymentReady(ctx context.Context, kubeconfig []byte, namespace, name string, timeout time.Duration) error {
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return err
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}

	return waitForCondition(timeout, 2*time.Second, func() bool {
		dep, err := clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false
		}
		return dep.Status.ReadyReplicas == *dep.Spec.Replicas
	})
}
