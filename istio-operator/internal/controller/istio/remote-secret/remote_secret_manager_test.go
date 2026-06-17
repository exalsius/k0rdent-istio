package remotesecret

import (
	"context"
	"strings"
	"testing"
	"time"

	kcmv1beta1 "github.com/K0rdent/kcm/api/v1beta1"
	"github.com/k0rdent/istio/istio-operator/internal/controller/istio"
	"github.com/k0rdent/istio/istio-operator/internal/controller/record"
	"github.com/k0rdent/istio/istio-operator/internal/k8s"
	"github.com/k0rdent/istio/istio-operator/internal/labels"
	mcluster "istio.io/istio/pkg/kube/multicluster"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testIstioSystemNamespace = "istio-system"

func TestTryCreateUsesClusterDeploymentNamespaceSecret(t *testing.T) {
	record.DefaultRecorder = events.NewFakeRecorder(16)
	istio.IstioSystemNamespace = testIstioSystemNamespace

	cd := readyClusterDeployment("tenant-a", "member-a", "member-a")
	credential := &kcmv1beta1.Credential{
		ObjectMeta: metav1.ObjectMeta{Name: "member-a", Namespace: "tenant-a"},
		Spec:       kcmv1beta1.CredentialSpec{},
	}

	tenantSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "member-a-kubeconfig", Namespace: "tenant-a"},
		Data:       map[string][]byte{"value": []byte("tenant-kubeconfig")},
	}

	defaultSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "member-a-kubeconfig", Namespace: k8s.DefaultKCMSystemNamespace},
		Data:       map[string][]byte{"value": []byte("default-kubeconfig")},
	}

	c := newFakeClient(t, credential, tenantSecret, defaultSecret)
	manager := NewFakeManager(c)

	err := manager.TryCreate(context.Background(), cd, CreateOptions{})
	if err != nil {
		t.Fatalf("TryCreate returned error: %v", err)
	}

	created := &corev1.Secret{}
	err = c.Get(context.Background(), client.ObjectKey{
		Name:      GetRemoteSecretName(cd.Name, cd.Namespace),
		Namespace: testIstioSystemNamespace,
	}, created)
	if err != nil {
		t.Fatalf("failed to get created remote secret: %v", err)
	}

	if got := created.Labels[mcluster.MultiClusterSecretLabel]; got != "false" {
		t.Fatalf("expected multicluster label to be false, got %q", got)
	}
}

func TestTryCreateSetsMultiClusterLabelTrueForRegionCluster(t *testing.T) {
	record.DefaultRecorder = events.NewFakeRecorder(16)
	istio.IstioSystemNamespace = testIstioSystemNamespace

	cd := readyClusterDeployment("tenant-a", "member-a", "member-a")
	cd.Labels = map[string]string{labels.KCMRegionClusterLabel: "true"}
	credential := &kcmv1beta1.Credential{
		ObjectMeta: metav1.ObjectMeta{Name: "member-a", Namespace: "tenant-a"},
		Spec:       kcmv1beta1.CredentialSpec{},
	}
	tenantSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "member-a-kubeconfig", Namespace: "tenant-a"},
		Data:       map[string][]byte{"value": []byte("tenant-kubeconfig")},
	}

	c := newFakeClient(t, credential, tenantSecret)
	manager := NewFakeManager(c)

	if err := manager.TryCreate(context.Background(), cd, CreateOptions{}); err != nil {
		t.Fatalf("TryCreate returned error: %v", err)
	}

	created := &corev1.Secret{}
	err := c.Get(context.Background(), client.ObjectKey{
		Name:      GetRemoteSecretName(cd.Name, cd.Namespace),
		Namespace: testIstioSystemNamespace,
	}, created)
	if err != nil {
		t.Fatalf("failed to get created remote secret: %v", err)
	}

	if got := created.Labels[mcluster.MultiClusterSecretLabel]; got != "true" {
		t.Fatalf("expected multicluster label to be true, got %q", got)
	}
}

func TestTryCreateFailsWhenSecretMissingInClusterDeploymentNamespace(t *testing.T) {
	record.DefaultRecorder = events.NewFakeRecorder(16)
	istio.IstioSystemNamespace = testIstioSystemNamespace

	cd := readyClusterDeployment("tenant-b", "member-b", "member-b")
	credential := &kcmv1beta1.Credential{
		ObjectMeta: metav1.ObjectMeta{Name: "member-b", Namespace: "tenant-b"},
		Spec:       kcmv1beta1.CredentialSpec{},
	}

	onlyDefaultSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "member-b-kubeconfig", Namespace: k8s.DefaultKCMSystemNamespace},
		Data:       map[string][]byte{"value": []byte("default-kubeconfig")},
	}

	c := newFakeClient(t, credential, onlyDefaultSecret)
	manager := NewFakeManager(c)

	err := manager.TryCreate(context.Background(), cd, CreateOptions{})
	if err == nil {
		t.Fatalf("expected TryCreate to fail when kubeconfig is missing in ClusterDeployment namespace")
	}

	if !strings.Contains(err.Error(), "failed to get kubeconfig from secret") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func readyClusterDeployment(namespace, name, credential string) *kcmv1beta1.ClusterDeployment {
	return &kcmv1beta1.ClusterDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: kcmv1beta1.ClusterDeploymentSpec{
			Template:   "aws-cluster-template",
			Credential: credential,
		},
		Status: kcmv1beta1.ClusterDeploymentStatus{
			Conditions: []metav1.Condition{
				{
					Type:               kcmv1beta1.ReadyCondition,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.Time{Time: time.Now()},
				},
				{
					Type:               kcmv1beta1.CAPIClusterSummaryCondition,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.Time{Time: time.Now()},
				},
			},
		},
	}
}

func newFakeClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()

	s := runtime.NewScheme()
	if err := k8sscheme.AddToScheme(s); err != nil {
		t.Fatalf("failed to add core scheme: %v", err)
	}
	if err := kcmv1beta1.AddToScheme(s); err != nil {
		t.Fatalf("failed to add kcm scheme: %v", err)
	}

	return fake.NewClientBuilder().WithScheme(s).WithObjects(objects...).Build()
}
