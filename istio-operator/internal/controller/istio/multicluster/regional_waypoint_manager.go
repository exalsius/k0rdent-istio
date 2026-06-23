package multicluster

import (
	"context"
	"fmt"

	kcmv1beta1 "github.com/K0rdent/kcm/api/v1beta1"
	"github.com/k0rdent/istio/istio-operator/internal/controller/istio"
	"github.com/k0rdent/istio/istio-operator/internal/controller/record"
	"github.com/k0rdent/istio/istio-operator/internal/controller/utils"
	"github.com/k0rdent/istio/istio-operator/internal/hash"
	"github.com/k0rdent/istio/istio-operator/internal/labels"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// RegionalWaypointManager manages MultiClusterService resources that mirror a child cluster's
// waypoint Gateway onto the KCM regional cluster that hosts it.
type RegionalWaypointManager struct {
	client client.Client
}

func NewRegionalWaypointManager(c client.Client) *RegionalWaypointManager {
	return &RegionalWaypointManager{client: c}
}

// TryCreate creates or updates the regional waypoint MCS for the given child cluster.
// regionClusterName is the ClusterDeployment name of the KCM regional cluster hosting this child.
func (m *RegionalWaypointManager) TryCreate(ctx context.Context, cd *kcmv1beta1.ClusterDeployment, regionClusterName string) error {
	log := log.FromContext(ctx)
	mcsName := RegionalWaypointMCSName(cd.Name, cd.Namespace)

	existing := new(kcmv1beta1.MultiClusterService)
	err := m.client.Get(ctx, types.NamespacedName{Name: mcsName}, existing)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to get regional waypoint MCS: %w", err)
	}

	mcs := m.generateRegionalWaypointMCS(cd, regionClusterName)

	if errors.IsNotFound(err) {
		log.Info("Creating regional waypoint MCS", "name", mcsName)
		if createErr := client.IgnoreAlreadyExists(m.client.Create(ctx, mcs)); createErr != nil {
			return fmt.Errorf("failed to create regional waypoint MCS: %w", createErr)
		}
		m.sendCreationEvent(cd, mcsName)
		log.Info("Regional waypoint MCS successfully created", "name", mcsName)
		return nil
	}

	if labels.IstioVersion(mcs.Labels) == labels.IstioVersion(existing.Labels) {
		return nil
	}

	existing.Spec = mcs.Spec
	existing.Labels = mcs.Labels
	if updateErr := m.client.Update(ctx, existing); updateErr != nil {
		return fmt.Errorf("failed to update regional waypoint MCS: %w", updateErr)
	}

	m.sendUpdateEvent(cd, mcsName)
	log.Info("Regional waypoint MCS successfully updated", "name", mcsName)
	return nil
}

// TryDelete deletes the regional waypoint MCS for the given request.
func (m *RegionalWaypointManager) TryDelete(ctx context.Context, req ctrl.Request) error {
	log := log.FromContext(ctx)
	mcsName := RegionalWaypointMCSName(req.Name, req.Namespace)

	log.Info("Trying to delete regional waypoint MCS", "name", mcsName)
	mcs := &kcmv1beta1.MultiClusterService{
		ObjectMeta: metav1.ObjectMeta{Name: mcsName},
	}

	if err := m.client.Delete(ctx, mcs); err != nil {
		if errors.IsNotFound(err) {
			log.Info("Regional waypoint MCS already deleted", "name", mcsName)
			return nil
		}
		return fmt.Errorf("failed to delete regional waypoint MCS: %w", err)
	}

	log.Info("Regional waypoint MCS successfully deleted", "name", mcsName)
	m.sendDeletionEvent(req, mcsName)
	return nil
}

func (m *RegionalWaypointManager) generateRegionalWaypointMCS(cd *kcmv1beta1.ClusterDeployment, regionClusterName string) *kcmv1beta1.MultiClusterService {
	waypointName := fmt.Sprintf("%s-waypoint", cd.Name)

	return &kcmv1beta1.MultiClusterService{
		ObjectMeta: metav1.ObjectMeta{
			Name: RegionalWaypointMCSName(cd.Name, cd.Namespace),
			Labels: map[string]string{
				labels.ClusterNameLabel:         cd.Name,
				labels.ClusterNamespaceLabel:    cd.Namespace,
				labels.K0rdentIstioVersionLabel: istio.ReleaseVersion,
				labels.ManagedByLabel:           labels.ManagedByIstioOperator,
			},
		},
		Spec: kcmv1beta1.MultiClusterServiceSpec{
			// Both keys are required: kcm-region-cluster guards against accidentally matching
			// a non-regional cluster that happens to carry the kof-cluster-name label, while
			// kof-cluster-name pins delivery to the one specific regional cluster.
			ClusterSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					labels.KCMRegionClusterLabel: "true",
					labels.KofClusterNameLabel:   regionClusterName,
				},
			},
			DependsOn: []string{
				GetNetworkMultiClusterServiceName(),
			},
			ServiceSpec: kcmv1beta1.ServiceSpec{
				Services: []kcmv1beta1.Service{
					{
						Name:      waypointName,
						Namespace: istio.IstioSystemNamespace,
						Template:  istio.ServiceTemplateName("propagation"),
						Values:    regionalWaypointValuesYAML(waypointName),
					},
				},
			},
		},
	}
}

// regionalWaypointValuesYAML returns Helm values for the k0rdent-istio-propagation chart
// that unconditionally deploys a waypoint Gateway. No Sveltos template expression is needed
// here because the MCS ClusterSelector already scopes delivery to the correct regional cluster.
//
// The block scalar content is indented at 14 spaces so that the YAML parser strips exactly
// that prefix — matching the convention used by other propagation value builders in this package.
func regionalWaypointValuesYAML(waypointName string) string {
	return fmt.Sprintf(`propagation:
  enabled: true
  data: |
              apiVersion: gateway.networking.k8s.io/v1
              kind: Gateway
              metadata:
                name: %s
                namespace: istio-system
                annotations:
                  networking.istio.io/service-type: ClusterIP
              spec:
                gatewayClassName: istio-waypoint
                listeners:
                - name: mesh
                  port: 15008
                  protocol: HBONE
                  allowedRoutes:
                    namespaces:
                      from: All
`, waypointName)
}

func (m *RegionalWaypointManager) sendCreationEvent(cd *kcmv1beta1.ClusterDeployment, mcsName string) {
	record.Eventf(
		cd,
		utils.GetEventsAnnotations(cd),
		"RegionalWaypointMCSCreated",
		"Regional waypoint MultiClusterService '%s' for cluster '%s' is successfully created",
		mcsName, cd.Name,
	)
}

func (m *RegionalWaypointManager) sendUpdateEvent(cd *kcmv1beta1.ClusterDeployment, mcsName string) {
	record.Eventf(
		cd,
		utils.GetEventsAnnotations(cd),
		"RegionalWaypointMCSUpdated",
		"Regional waypoint MultiClusterService '%s' for cluster '%s' is successfully updated",
		mcsName, cd.Name,
	)
}

func (m *RegionalWaypointManager) sendDeletionEvent(req ctrl.Request, mcsName string) {
	cd := utils.GetClusterDeploymentStub(req.Name, req.Namespace)
	record.Eventf(
		cd,
		nil,
		"RegionalWaypointMCSDeleted",
		"Regional waypoint MultiClusterService '%s' for cluster '%s' is successfully deleted",
		mcsName, req.Name,
	)
}

// RegionalWaypointMCSName returns the stable hashed name for the MultiClusterService that
// mirrors the child cluster's waypoint onto the associated regional cluster.
func RegionalWaypointMCSName(clusterName, namespace string) string {
	name := fmt.Sprintf("%s-%s", namespace, clusterName)
	return hash.WithPrefix("istio-regional-waypoint", name, hash.AdlerHash)
}

// GetNetworkMultiClusterServiceName returns the name of the MCS that installs the full
// Istio distribution on member clusters. The regional waypoint depends on this being applied first.
func GetNetworkMultiClusterServiceName() string {
	return fmt.Sprintf("%s-network", istio.IstioReleaseName)
}
