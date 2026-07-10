package multicluster

import (
	"context"
	"fmt"

	kcmv1beta1 "github.com/K0rdent/kcm/api/v1beta1"
	"github.com/k0rdent/istio/istio-operator/internal/controller/istio"
	"github.com/k0rdent/istio/istio-operator/internal/controller/istio/cert"
	remotesecret "github.com/k0rdent/istio/istio-operator/internal/controller/istio/remote-secret"
	"github.com/k0rdent/istio/istio-operator/internal/controller/record"
	"github.com/k0rdent/istio/istio-operator/internal/controller/utils"
	"github.com/k0rdent/istio/istio-operator/internal/hash"
	"github.com/k0rdent/istio/istio-operator/internal/labels"
	addoncontrollerv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type RemoteSecretPropagationManager struct {
	client client.Client
}

func New(c client.Client) *RemoteSecretPropagationManager {
	return &RemoteSecretPropagationManager{
		client: c,
	}
}

func (m *RemoteSecretPropagationManager) TryCreate(ctx context.Context, clusterDeployment *kcmv1beta1.ClusterDeployment) error {
	log := log.FromContext(ctx)

	if err := m.tryDeleteDeprecatedPropagationMCS(ctx, clusterDeployment.Name, clusterDeployment.Namespace); err != nil {
		log.Error(err, "Failed to delete deprecated MultiClusterService for secret propagation")
	}

	mcs, err := m.getMultiClusterService(ctx, clusterDeployment.Name, clusterDeployment.Namespace)
	if err != nil {
		return fmt.Errorf("failed to get MultiClusterService: %w", err)
	}

	if mcs != nil {
		log.Info("Trying to update MultiClusterService for secret propagation")

		if err := m.updateMultiClusterService(ctx, clusterDeployment, mcs); err != nil {
			return fmt.Errorf("failed to update MultiClusterService: %w", err)
		}

		return nil
	}

	log.Info("Trying to create MultiClusterService for secret propagation")
	if err := m.createMultiClusterService(ctx, clusterDeployment); err != nil {
		return fmt.Errorf("failed to create MultiClusterService resource: %w", err)
	}

	m.sendCreationEvent(clusterDeployment)
	log.Info("MultiClusterService successfully created")
	return nil
}

// EnsureDeleted requests deletion of the MultiClusterService and reports whether
// it is fully gone. Deletion is asynchronous: KCM holds a finalizer on the MCS
// until Sveltos has withdrawn the propagated services from all member clusters,
// and that withdrawal re-renders the propagation templates from the source
// secrets referenced via templateResourceRefs. Callers must therefore keep the
// remote secret and CA secret alive until this returns true, otherwise the MCS
// is stuck terminating forever.
func (m *RemoteSecretPropagationManager) EnsureDeleted(ctx context.Context, req ctrl.Request) (bool, error) {
	log := log.FromContext(ctx)

	if err := m.tryDeleteDeprecatedPropagationMCS(ctx, req.Name, req.Namespace); err != nil {
		log.Error(err, "Failed to delete deprecated MultiClusterService for secret propagation")
	}

	mcs := new(kcmv1beta1.MultiClusterService)
	if err := m.client.Get(ctx, types.NamespacedName{
		Name: MultiClusterServiceName(req.Name, req.Namespace),
	}, mcs); err != nil {
		if errors.IsNotFound(err) {
			log.Info("MultiClusterService already deleted")
			return true, nil
		}
		return false, fmt.Errorf("failed to get MultiClusterService: %w", err)
	}

	if !mcs.DeletionTimestamp.IsZero() {
		log.Info("MultiClusterService deletion still in progress")
		return false, nil
	}

	log.Info("Trying to delete MultiClusterService for secret propagation")
	if err := m.client.Delete(ctx, mcs); err != nil && !errors.IsNotFound(err) {
		return false, fmt.Errorf("failed to delete MultiClusterService: %w", err)
	}

	m.sendDeletionEvent(req)
	log.Info("MultiClusterService deletion initiated")

	return false, nil
}

func (m *RemoteSecretPropagationManager) updateMultiClusterService(ctx context.Context, cd *kcmv1beta1.ClusterDeployment, oldMCS *kcmv1beta1.MultiClusterService) error {
	newMCS := m.generateMultiClusterService(cd)

	if labels.IstioVersion(newMCS.Labels) == labels.IstioVersion(oldMCS.Labels) {
		return nil
	}

	oldMCS.Spec = newMCS.Spec
	oldMCS.Labels = newMCS.Labels
	if err := m.client.Update(ctx, oldMCS); err != nil {
		return fmt.Errorf("failed to update MultiClusterService: %w", err)
	}

	m.sendUpdateEvent(cd)
	return nil
}

func (m *RemoteSecretPropagationManager) getMultiClusterService(ctx context.Context, name, namespace string) (*kcmv1beta1.MultiClusterService, error) {
	mcs := new(kcmv1beta1.MultiClusterService)
	if err := m.client.Get(ctx, types.NamespacedName{
		Name: MultiClusterServiceName(name, namespace),
	}, mcs); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return mcs, nil
}

func (m *RemoteSecretPropagationManager) createMultiClusterService(ctx context.Context, cd *kcmv1beta1.ClusterDeployment) error {
	mcs := m.generateMultiClusterService(cd)
	return client.IgnoreAlreadyExists(m.client.Create(ctx, mcs))
}

func (m *RemoteSecretPropagationManager) generateMultiClusterService(cd *kcmv1beta1.ClusterDeployment) *kcmv1beta1.MultiClusterService {
	// One per-cluster MCS ships both discovery and CA material.
	remoteIdentifier := "RemoteSecretData"
	caIdentifier := "CASecretData"
	remoteValuesYAML := utils.MustPropagationServiceValuesYAML(remoteIdentifier)

	remoteSecretName := remotesecret.GetRemoteSecretName(cd.Name, cd.Namespace)
	caSecretName := cert.GetCASecretName(cd.Name, cd.Namespace)
	caValuesYAML := utils.MustScopedCAPropagationServiceValuesYAML(caIdentifier, caSecretName)

	mcs := &kcmv1beta1.MultiClusterService{
		ObjectMeta: metav1.ObjectMeta{
			Name: MultiClusterServiceName(cd.Name, cd.Namespace),
			Labels: map[string]string{
				labels.ClusterNameLabel:         cd.Name,
				labels.ClusterNamespaceLabel:    cd.Namespace,
				labels.K0rdentIstioVersionLabel: istio.ReleaseVersion,
				labels.ManagedByLabel:           labels.ManagedByIstioOperator,
			},
		},
		Spec: kcmv1beta1.MultiClusterServiceSpec{
			ClusterSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					labels.IstioRoleLabel: labels.IstioRoleLabelMemberValue,
				},
			},
			DependsOn: []string{
				GetNamespaceMultiClusterServiceName(),
			},
			ServiceSpec: kcmv1beta1.ServiceSpec{
				Services: []kcmv1beta1.Service{
					{
						Name:      remoteSecretName,
						Namespace: istio.IstioSystemNamespace,
						Template:  istio.ServiceTemplateName("propagation"),
						Values:    remoteValuesYAML,
					},
					{
						Name:      caSecretName,
						Namespace: istio.IstioSystemNamespace,
						Template:  istio.ServiceTemplateName("propagation"),
						Values:    caValuesYAML,
					},
				},
				// Refs are required so template rendering fails fast if source
				// management secrets are missing.
				TemplateResourceRefs: []addoncontrollerv1beta1.TemplateResourceRef{
					{
						Identifier: remoteIdentifier,
						Resource: corev1.ObjectReference{
							APIVersion: "v1",
							Kind:       "Secret",
							Name:       remoteSecretName,
							Namespace:  istio.IstioSystemNamespace,
						},
					},
					{
						Identifier: caIdentifier,
						Resource: corev1.ObjectReference{
							APIVersion: "v1",
							Kind:       "Secret",
							Name:       caSecretName,
							Namespace:  istio.IstioSystemNamespace,
						},
					},
				},
			},
		},
	}

	if utils.IsInMesh(cd) {
		// If cluster is in mesh, set selector to propagate only to clusters in same mesh
		mcs.Spec.ClusterSelector.MatchLabels[labels.IstioMeshLabel] = cd.Labels[labels.IstioMeshLabel]
	} else {
		mcs.Spec.ClusterSelector.MatchExpressions = []metav1.LabelSelectorRequirement{
			{
				Key:      labels.IstioMeshLabel,
				Operator: metav1.LabelSelectorOpDoesNotExist,
			},
		}
	}

	return mcs
}

// tryDeleteDeprecatedPropagationMCS attempts to delete the MultiClusterService created by older versions of the operator for secret propagation,
// which had a different naming scheme. This is needed to ensure cleanup of the old MCS.
func (m *RemoteSecretPropagationManager) tryDeleteDeprecatedPropagationMCS(ctx context.Context, name, namespace string) error {
	mcs := new(kcmv1beta1.MultiClusterService)
	if err := m.client.Get(ctx, types.NamespacedName{
		Name: getDeprecatedMultiClusterServiceName(name, namespace),
	}, mcs); err != nil {
		return client.IgnoreNotFound(err)
	}

	if !utils.IsResourceCreatedByOperator(mcs) {
		return nil
	}

	return m.client.Delete(ctx, mcs)
}

func (m *RemoteSecretPropagationManager) sendUpdateEvent(cd *kcmv1beta1.ClusterDeployment) {
	record.Eventf(
		cd,
		utils.GetEventsAnnotations(cd),
		"MultiClusterServiceUpdated",
		"MultiClusterService '%s' for secret propagation is successfully updated",
		MultiClusterServiceName(cd.Name, cd.Namespace),
	)
}

func (m *RemoteSecretPropagationManager) sendCreationEvent(cd *kcmv1beta1.ClusterDeployment) {
	record.Eventf(
		cd,
		utils.GetEventsAnnotations(cd),
		"MultiClusterServiceCreated",
		"MultiClusterService '%s' for secret propagation is successfully created",
		MultiClusterServiceName(cd.Name, cd.Namespace),
	)
}

func (m *RemoteSecretPropagationManager) sendDeletionEvent(req ctrl.Request) {
	cd := utils.GetClusterDeploymentStub(req.Name, req.Namespace)
	record.Eventf(
		cd,
		nil,
		"MultiClusterServiceDeleted",
		"MultiClusterService '%s' for secret propagation is successfully deleted",
		MultiClusterServiceName(req.Name, req.Namespace),
	)
}

func getDeprecatedMultiClusterServiceName(clusterName, namespace string) string {
	name := multiClusterServiceKey(clusterName, namespace)
	return hash.WithPrefix("remote-secret-propagation", name, hash.FnvHash)
}

// multiClusterServiceKey returns the canonical "namespace-name" key for the given cluster.
func multiClusterServiceKey(clusterName, namespace string) string {
	return fmt.Sprintf("%s-%s", namespace, clusterName)
}

// MultiClusterServiceName returns the unique hashed name for the MultiClusterService
// managing secret propagation for the given cluster.
func MultiClusterServiceName(clusterName, namespace string) string {
	name := multiClusterServiceKey(clusterName, namespace)
	return hash.WithPrefix("istio-secrets-propagation", name, hash.AdlerHash)
}

func GetNamespaceMultiClusterServiceName() string {
	return fmt.Sprintf("%s-namespace", istio.IstioReleaseName)
}
