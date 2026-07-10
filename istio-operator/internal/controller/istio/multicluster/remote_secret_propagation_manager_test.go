package multicluster

import (
	"context"
	"testing"

	kcmv1beta1 "github.com/K0rdent/kcm/api/v1beta1"
	"github.com/k0rdent/istio/istio-operator/internal/controller/istio"
	"github.com/k0rdent/istio/istio-operator/internal/controller/record"
	"github.com/k0rdent/istio/istio-operator/internal/labels"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testUpgradedVersion = "1.1.0"

func TestTryCreate_CreatesMCSWhenNotExists(t *testing.T) {
	setupTest(t, "1.0.0")

	cd := newClusterDeployment()
	c := newFakeClient(t)
	manager := New(c)

	if err := manager.TryCreate(context.Background(), cd); err != nil {
		t.Fatalf("TryCreate returned error: %v", err)
	}

	mcs := &kcmv1beta1.MultiClusterService{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: MultiClusterServiceName(cd.Name, cd.Namespace),
	}, mcs); err != nil {
		t.Fatalf("MCS was not created: %v", err)
	}

	if got := mcs.Labels[labels.K0rdentIstioVersionLabel]; got != "1.0.0" {
		t.Errorf("expected version label %q, got %q", "1.0.0", got)
	}
}

func TestTryCreate_UpdatesMCSWhenVersionChanges(t *testing.T) {
	setupTest(t, "1.0.0")

	cd := newClusterDeployment()

	// Pre-create MCS with old version label.
	existingMCS := &kcmv1beta1.MultiClusterService{
		ObjectMeta: metav1.ObjectMeta{
			Name: MultiClusterServiceName(cd.Name, cd.Namespace),
			Labels: map[string]string{
				labels.K0rdentIstioVersionLabel: "1.0.0",
			},
		},
	}
	c := newFakeClient(t, existingMCS)
	manager := New(c)

	// Simulate a version upgrade.
	istio.ReleaseVersion = testUpgradedVersion

	if err := manager.TryCreate(context.Background(), cd); err != nil {
		t.Fatalf("TryCreate returned error: %v", err)
	}

	mcs := &kcmv1beta1.MultiClusterService{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: MultiClusterServiceName(cd.Name, cd.Namespace),
	}, mcs); err != nil {
		t.Fatalf("failed to get MCS: %v", err)
	}

	if got := mcs.Labels[labels.K0rdentIstioVersionLabel]; got != testUpgradedVersion {
		t.Errorf("expected updated version label %q, got %q", testUpgradedVersion, got)
	}
}

func TestTryCreate_SkipsUpdateWhenVersionUnchanged(t *testing.T) {
	setupTest(t, "1.0.0")

	cd := newClusterDeployment()

	existingMCS := &kcmv1beta1.MultiClusterService{
		ObjectMeta: metav1.ObjectMeta{
			Name:            MultiClusterServiceName(cd.Name, cd.Namespace),
			ResourceVersion: "999",
			Labels: map[string]string{
				labels.K0rdentIstioVersionLabel: "1.0.0",
			},
		},
	}
	c := newFakeClient(t, existingMCS)
	manager := New(c)

	if err := manager.TryCreate(context.Background(), cd); err != nil {
		t.Fatalf("TryCreate returned error: %v", err)
	}

	mcs := &kcmv1beta1.MultiClusterService{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: MultiClusterServiceName(cd.Name, cd.Namespace),
	}, mcs); err != nil {
		t.Fatalf("failed to get MCS: %v", err)
	}

	// ResourceVersion must not change — no update was issued.
	if mcs.ResourceVersion != "999" {
		t.Errorf("expected ResourceVersion %q (no update), got %q", "999", mcs.ResourceVersion)
	}
}

func TestTryCreate_SequentialVersionUpgrades(t *testing.T) {
	versions := []string{"1.0.0", testUpgradedVersion, "2.0.0", "2.1.0"}

	cd := newClusterDeployment()
	c := newFakeClient(t)
	manager := New(c)

	for _, version := range versions {
		setupTest(t, version)

		if err := manager.TryCreate(context.Background(), cd); err != nil {
			t.Fatalf("TryCreate returned error at version %s: %v", version, err)
		}

		mcs := &kcmv1beta1.MultiClusterService{}
		if err := c.Get(context.Background(), types.NamespacedName{
			Name: MultiClusterServiceName(cd.Name, cd.Namespace),
		}, mcs); err != nil {
			t.Fatalf("failed to get MCS at version %s: %v", version, err)
		}

		if got := mcs.Labels[labels.K0rdentIstioVersionLabel]; got != version {
			t.Errorf("version %s: expected label %q, got %q", version, version, got)
		}
	}
}

// setupTest initialises shared global state required by the manager under test.
func setupTest(t *testing.T, releaseVersion string) {
	t.Helper()
	record.DefaultRecorder = events.NewFakeRecorder(16)
	istio.IstioSystemNamespace = "istio-system"
	istio.IstioReleaseName = "test-istio"
	istio.ReleaseVersion = releaseVersion
}

func newClusterDeployment() *kcmv1beta1.ClusterDeployment {
	return &kcmv1beta1.ClusterDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-a",
			Namespace: "default",
			Labels: map[string]string{
				labels.IstioRoleLabel: labels.IstioRoleLabelMemberValue,
			},
		},
		Spec: kcmv1beta1.ClusterDeploymentSpec{
			Template:   "test-template",
			Credential: "test-credential",
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

	return fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objects...).
		Build()
}
