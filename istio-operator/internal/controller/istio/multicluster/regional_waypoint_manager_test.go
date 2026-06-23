package multicluster

import (
	"context"
	"strings"
	"testing"

	kcmv1beta1 "github.com/K0rdent/kcm/api/v1beta1"
	"github.com/k0rdent/istio/istio-operator/internal/controller/istio"
	"github.com/k0rdent/istio/istio-operator/internal/labels"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	testRegionClusterName = "region-cluster-a"
	testUpgradedVersion   = "1.1.0"
)

func TestRegionalWaypoint_CreatesMCSWhenNotExists(t *testing.T) {
	setupTest(t, "1.0.0")

	cd := newClusterDeployment()
	c := newFakeClient(t)
	manager := NewRegionalWaypointManager(c)

	if err := manager.TryCreate(context.Background(), cd, testRegionClusterName); err != nil {
		t.Fatalf("TryCreate returned error: %v", err)
	}

	mcs := &kcmv1beta1.MultiClusterService{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: RegionalWaypointMCSName(cd.Name, cd.Namespace),
	}, mcs); err != nil {
		t.Fatalf("MCS was not created: %v", err)
	}

	if got := mcs.Labels[labels.K0rdentIstioVersionLabel]; got != "1.0.0" {
		t.Errorf("version label: expected %q, got %q", "1.0.0", got)
	}
}

func TestRegionalWaypoint_ClusterSelectorTargetsRegionByKofClusterNameLabel(t *testing.T) {
	setupTest(t, "1.0.0")

	cd := newClusterDeployment()
	c := newFakeClient(t)
	manager := NewRegionalWaypointManager(c)

	if err := manager.TryCreate(context.Background(), cd, testRegionClusterName); err != nil {
		t.Fatalf("TryCreate returned error: %v", err)
	}

	mcs := &kcmv1beta1.MultiClusterService{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: RegionalWaypointMCSName(cd.Name, cd.Namespace),
	}, mcs); err != nil {
		t.Fatalf("failed to get MCS: %v", err)
	}

	ml := mcs.Spec.ClusterSelector.MatchLabels

	// kof-cluster-name pins delivery to exactly one regional cluster.
	if got := ml[labels.KofClusterNameLabel]; got != testRegionClusterName {
		t.Errorf("ClusterSelector[%s]: expected %q, got %q",
			labels.KofClusterNameLabel, testRegionClusterName, got)
	}
	// kcm-region-cluster guards against matching non-regional clusters.
	if got := ml[labels.KCMRegionClusterLabel]; got != "true" {
		t.Errorf("ClusterSelector[%s]: expected %q, got %q",
			labels.KCMRegionClusterLabel, "true", got)
	}
	if len(ml) != 2 {
		t.Errorf("ClusterSelector should have exactly 2 label keys, got %d: %v", len(ml), ml)
	}
	if len(mcs.Spec.ClusterSelector.MatchExpressions) != 0 {
		t.Errorf("ClusterSelector should have no MatchExpressions, got %v",
			mcs.Spec.ClusterSelector.MatchExpressions)
	}
}

func TestRegionalWaypoint_WaypointNamedAfterChildCluster(t *testing.T) {
	setupTest(t, "1.0.0")

	cd := newClusterDeployment()
	c := newFakeClient(t)
	manager := NewRegionalWaypointManager(c)

	if err := manager.TryCreate(context.Background(), cd, testRegionClusterName); err != nil {
		t.Fatalf("TryCreate returned error: %v", err)
	}

	mcs := &kcmv1beta1.MultiClusterService{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: RegionalWaypointMCSName(cd.Name, cd.Namespace),
	}, mcs); err != nil {
		t.Fatalf("failed to get MCS: %v", err)
	}

	if len(mcs.Spec.ServiceSpec.Services) != 1 {
		t.Fatalf("expected 1 service entry, got %d", len(mcs.Spec.ServiceSpec.Services))
	}

	svc := mcs.Spec.ServiceSpec.Services[0]
	expectedName := cd.Name + "-waypoint"
	if svc.Name != expectedName {
		t.Errorf("service name: expected %q, got %q", expectedName, svc.Name)
	}

	// Values must reference the waypoint name so the propagation chart renders the right Gateway.
	if !strings.Contains(svc.Values, expectedName) {
		t.Errorf("service values do not contain waypoint name %q:\n%s", expectedName, svc.Values)
	}
}

func TestRegionalWaypoint_DependsOnNetworkMCS(t *testing.T) {
	setupTest(t, "1.0.0")

	cd := newClusterDeployment()
	c := newFakeClient(t)
	manager := NewRegionalWaypointManager(c)

	if err := manager.TryCreate(context.Background(), cd, testRegionClusterName); err != nil {
		t.Fatalf("TryCreate returned error: %v", err)
	}

	mcs := &kcmv1beta1.MultiClusterService{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: RegionalWaypointMCSName(cd.Name, cd.Namespace),
	}, mcs); err != nil {
		t.Fatalf("failed to get MCS: %v", err)
	}

	expectedDep := GetNetworkMultiClusterServiceName()
	if len(mcs.Spec.DependsOn) != 1 || mcs.Spec.DependsOn[0] != expectedDep {
		t.Errorf("DependsOn: expected [%q], got %v", expectedDep, mcs.Spec.DependsOn)
	}
}

func TestRegionalWaypoint_UsesPropagationTemplate(t *testing.T) {
	setupTest(t, "1.0.0")

	cd := newClusterDeployment()
	c := newFakeClient(t)
	manager := NewRegionalWaypointManager(c)

	if err := manager.TryCreate(context.Background(), cd, testRegionClusterName); err != nil {
		t.Fatalf("TryCreate returned error: %v", err)
	}

	mcs := &kcmv1beta1.MultiClusterService{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: RegionalWaypointMCSName(cd.Name, cd.Namespace),
	}, mcs); err != nil {
		t.Fatalf("failed to get MCS: %v", err)
	}

	expectedTemplate := istio.ServiceTemplateName("propagation")
	if got := mcs.Spec.ServiceSpec.Services[0].Template; got != expectedTemplate {
		t.Errorf("service template: expected %q, got %q", expectedTemplate, got)
	}
}

func TestRegionalWaypoint_UpdatesMCSWhenVersionChanges(t *testing.T) {
	setupTest(t, "1.0.0")

	cd := newClusterDeployment()
	existingMCS := &kcmv1beta1.MultiClusterService{
		ObjectMeta: metav1.ObjectMeta{
			Name: RegionalWaypointMCSName(cd.Name, cd.Namespace),
			Labels: map[string]string{
				labels.K0rdentIstioVersionLabel: "1.0.0",
			},
		},
	}
	c := newFakeClient(t, existingMCS)
	manager := NewRegionalWaypointManager(c)

	istio.ReleaseVersion = testUpgradedVersion

	if err := manager.TryCreate(context.Background(), cd, testRegionClusterName); err != nil {
		t.Fatalf("TryCreate returned error: %v", err)
	}

	mcs := &kcmv1beta1.MultiClusterService{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: RegionalWaypointMCSName(cd.Name, cd.Namespace),
	}, mcs); err != nil {
		t.Fatalf("failed to get MCS: %v", err)
	}

	if got := mcs.Labels[labels.K0rdentIstioVersionLabel]; got != testUpgradedVersion {
		t.Errorf("expected updated version label %q, got %q", testUpgradedVersion, got)
	}
}

func TestRegionalWaypoint_SkipsUpdateWhenVersionUnchanged(t *testing.T) {
	setupTest(t, "1.0.0")

	cd := newClusterDeployment()
	existingMCS := &kcmv1beta1.MultiClusterService{
		ObjectMeta: metav1.ObjectMeta{
			Name:            RegionalWaypointMCSName(cd.Name, cd.Namespace),
			ResourceVersion: "999",
			Labels: map[string]string{
				labels.K0rdentIstioVersionLabel: "1.0.0",
			},
		},
	}
	c := newFakeClient(t, existingMCS)
	manager := NewRegionalWaypointManager(c)

	if err := manager.TryCreate(context.Background(), cd, testRegionClusterName); err != nil {
		t.Fatalf("TryCreate returned error: %v", err)
	}

	mcs := &kcmv1beta1.MultiClusterService{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: RegionalWaypointMCSName(cd.Name, cd.Namespace),
	}, mcs); err != nil {
		t.Fatalf("failed to get MCS: %v", err)
	}

	// ResourceVersion must not change — no update was issued.
	if mcs.ResourceVersion != "999" {
		t.Errorf("expected ResourceVersion %q (no update), got %q", "999", mcs.ResourceVersion)
	}
}

func TestRegionalWaypoint_TryDelete_RemovesExistingMCS(t *testing.T) {
	setupTest(t, "1.0.0")

	cd := newClusterDeployment()
	existingMCS := &kcmv1beta1.MultiClusterService{
		ObjectMeta: metav1.ObjectMeta{
			Name: RegionalWaypointMCSName(cd.Name, cd.Namespace),
		},
	}
	c := newFakeClient(t, existingMCS)
	manager := NewRegionalWaypointManager(c)

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: cd.Name, Namespace: cd.Namespace}}
	if err := manager.TryDelete(context.Background(), req); err != nil {
		t.Fatalf("TryDelete returned error: %v", err)
	}

	mcs := &kcmv1beta1.MultiClusterService{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: RegionalWaypointMCSName(cd.Name, cd.Namespace),
	}, mcs); err == nil {
		t.Error("expected MCS to be deleted, but it still exists")
	}
}

func TestRegionalWaypoint_TryDelete_IdempotentForMissingMCS(t *testing.T) {
	setupTest(t, "1.0.0")

	cd := newClusterDeployment()
	c := newFakeClient(t) // no pre-existing MCS
	manager := NewRegionalWaypointManager(c)

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: cd.Name, Namespace: cd.Namespace}}
	if err := manager.TryDelete(context.Background(), req); err != nil {
		t.Errorf("TryDelete should not error for a missing MCS, got: %v", err)
	}
}
