package remotesecret

import (
	"context"
	"fmt"

	kcmv1beta1 "github.com/K0rdent/kcm/api/v1beta1"
	"github.com/k0rdent/istio/istio-operator/internal/controller/istio"
	"github.com/k0rdent/istio/istio-operator/internal/controller/record"
	"github.com/k0rdent/istio/istio-operator/internal/controller/utils"
	"github.com/k0rdent/istio/istio-operator/internal/hash"
	"github.com/k0rdent/istio/istio-operator/internal/k8s"
	"github.com/k0rdent/istio/istio-operator/internal/labels"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type CreateOptions struct {
	// AllowOverwrite enforces creation of a new remote secret and service account
	// token secret even if they already exists.
	AllowOverwrite bool
}

type RemoteSecretManager struct {
	client  client.Client
	creator RemoteSecretCreator
}

func New(c client.Client) *RemoteSecretManager {
	return &RemoteSecretManager{
		client:  c,
		creator: newDefaultRemoteSecretCreator(),
	}
}

// TryDelete deletes the remote secret for the given request.
func (rs *RemoteSecretManager) TryDelete(ctx context.Context, request ctrl.Request) error {
	log := log.FromContext(ctx)
	log.Info("Trying to delete remote secret")

	if err := rs.deleteDeprecatedSecret(ctx, request.Name, request.Namespace); err != nil {
		log.Error(err, "Failed to delete deprecated remote secret")
	}

	if err := rs.client.Delete(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      GetRemoteSecretName(request.Name, request.Namespace),
			Namespace: istio.IstioSystemNamespace,
		},
	}); err != nil {
		if errors.IsNotFound(err) {
			log.Info("Remote secret already deleted")
			return nil
		}
		return err
	}

	rs.sendDeletionEvent(request)
	log.Info("Remote secret successfully deleted")
	return nil
}

// TryCreate creates a remote secret for the given cluster deployment.
func (rs *RemoteSecretManager) TryCreate(ctx context.Context, clusterDeployment *kcmv1beta1.ClusterDeployment, opt CreateOptions) error {
	log := log.FromContext(ctx)
	log.Info("Trying to create remote secret")

	if err := rs.deleteDeprecatedSecret(ctx, clusterDeployment.Name, clusterDeployment.Namespace); err != nil {
		log.Error(err, "Failed to delete deprecated remote secret")
	}

	if !utils.IsClusterDeploymentReady(clusterDeployment) {
		log.Info("Cluster deployment is not ready")
		return nil
	}

	if !opt.AllowOverwrite {
		exists, err := rs.remoteSecretExists(ctx, clusterDeployment)
		if err != nil {
			return fmt.Errorf("failed to check remote secret: %w", err)
		}

		if exists {
			log.Info("Remote secret already exists")
			return nil
		}
	}

	createdInKCMRegion := k8s.CreatedInKCMRegion(clusterDeployment)

	regionKubeClient := rs.client

	if createdInKCMRegion {
		regionClusterName, err := k8s.GetKcmRegionClusterNameRelatedToClusterDeployment(ctx, rs.client, clusterDeployment)
		if err != nil {
			return fmt.Errorf("failed to get cluster region: %w", err)
		}

		regionKubeconfig, err := k8s.GetKubeconfigByRegionName(ctx, rs.client, regionClusterName)
		if err != nil {
			return fmt.Errorf("failed to get kubeconfig by region name: %w", err)
		}

		if regionKubeconfig == nil {
			return fmt.Errorf("no kubeconfig found for region cluster name: %s", regionClusterName)
		}

		regionClient, err := k8s.NewKubeClientFromKubeconfig(regionKubeconfig)
		if err != nil {
			return fmt.Errorf("failed to create kube client from region kubeconfig: %w", err)
		}

		regionKubeClient = regionClient.Client
	}

	kubeconfigSecretName, err := k8s.GetKubeconfigSecretName(ctx, rs.client, clusterDeployment)
	if err != nil {
		return fmt.Errorf("failed to get kubeconfig secret name: %w", err)
	}

	kubeconfig, err := k8s.GetKubeconfigFromSecretInNamespace(ctx, regionKubeClient, kubeconfigSecretName, clusterDeployment.Namespace)
	if err != nil {
		return fmt.Errorf("failed to get kubeconfig from secret: %w", err)
	}

	remoteSecret, err := rs.creator.GetRemoteSecret(ctx, kubeconfig, clusterDeployment, opt)
	if err != nil {
		return fmt.Errorf("failed to get remote secret: %w", err)
	}

	if err := rs.createSecretResource(ctx, remoteSecret); err != nil {
		log.Error(err, "failed to create remote secret")
		return fmt.Errorf("failed to create remote secret: %w", err)
	}

	rs.sendCreationEvent(clusterDeployment)
	log.Info("Remote secret successfully created")
	return nil
}

func (rs *RemoteSecretManager) remoteSecretExists(ctx context.Context, cd *kcmv1beta1.ClusterDeployment) (bool, error) {
	secret := &corev1.Secret{}
	secretName := GetRemoteSecretName(cd.Name, cd.Namespace)
	return utils.IsResourceExists(ctx, rs.client, secret, secretName, istio.IstioSystemNamespace)
}

// createSecretResource deletes any existing secret and recreates it.
func (rs *RemoteSecretManager) createSecretResource(ctx context.Context, secret *corev1.Secret) error {
	if err := client.IgnoreNotFound(rs.client.Delete(ctx, secret)); err != nil {
		return err
	}

	if err := rs.client.Create(ctx, secret); err != nil {
		return err
	}

	return nil
}

func (rs *RemoteSecretManager) deleteDeprecatedSecret(ctx context.Context, name, namespace string) error {
	secret := new(corev1.Secret)
	if err := rs.client.Get(ctx, types.NamespacedName{
		Name:      getDeprecatedRemoteSecretName(name, namespace),
		Namespace: istio.IstioSystemNamespace,
	}, secret); err != nil {
		return client.IgnoreNotFound(err)
	}

	if !utils.IsResourceCreatedByOperator(secret) {
		return nil
	}

	return rs.client.Delete(ctx, secret)
}

// TryCreateForLocalCluster creates a remote secret for the local (management) cluster using the provided KubeClient.
// Unlike TryCreate, it does not look up a kubeconfig secret from Kubernetes; instead it uses the provided client directly.
// serverOverride, when non-empty, replaces the server URL in the kubeconfig embedded in the secret.
// This must be set to the externally accessible API server address so that Istiod on member clusters
// can reach the management cluster across cluster boundaries.
func (rs *RemoteSecretManager) TryCreateForLocalCluster(ctx context.Context, kubeClient *k8s.KubeClient, clusterName, clusterNamespace, serverOverride string) error {
	log := log.FromContext(ctx)
	log.Info("Trying to create remote secret for management cluster")

	secretName := GetRemoteSecretName(clusterName, clusterNamespace)
	secret := &corev1.Secret{}
	exists, err := utils.IsResourceExists(ctx, rs.client, secret, secretName, istio.IstioSystemNamespace)
	if err != nil {
		return fmt.Errorf("failed to check remote secret: %w", err)
	}

	if exists {
		log.Info("Remote secret already exists for management cluster; rotating to refresh token")
	}

	remoteSecret, warn, err := CreateRemoteSecret(
		ctx,
		RemoteSecretOptions{
			AllowOverwrite: true,
			MultiCluster:   true,
			Type:           SecretTypeRemote,
			AuthType:       RemoteSecretAuthTypeBearerToken,
			ClusterName:    clusterName,
			ServerOverride: serverOverride,
			KubeOptions: KubeOptions{
				Namespace: istio.IstioSystemNamespace,
			},
		},
		clusterNamespace,
		kubeClient,
	)
	if err != nil {
		return fmt.Errorf("failed to get remote secret: %w", err)
	}

	if warn != nil {
		log.Info("Warning when generating remote secret for management cluster", "warning", warn)
	}

	if err := rs.createSecretResource(ctx, remoteSecret); err != nil {
		return fmt.Errorf("failed to create remote secret resource: %w", err)
	}

	log.Info("Remote secret successfully created for management cluster")
	return nil
}

func (rs *RemoteSecretManager) sendCreationEvent(cd *kcmv1beta1.ClusterDeployment) {
	record.Eventf(
		cd,
		utils.GetEventsAnnotations(cd),
		"SecretCreated",
		"Istio remote secret '%s' is successfully created",
		GetRemoteSecretName(cd.Name, cd.Namespace),
	)
}

func (rs *RemoteSecretManager) sendDeletionEvent(req ctrl.Request) {
	cd := utils.GetClusterDeploymentStub(req.Name, req.Namespace)
	record.Eventf(
		cd,
		nil,
		"SecretDeleted",
		"Istio remote secret '%s' is successfully deleted",
		GetRemoteSecretName(cd.Name, cd.Namespace),
	)
}

func getDeprecatedRemoteSecretName(clusterName, namespace string) string {
	name := fmt.Sprintf("%s-%s", namespace, clusterName)
	return hash.WithPrefix(remoteSecretPrefix, name, hash.FnvHash)
}

// RemoteSecretCreator creates a remote secret from a kubeconfig and cluster deployment.
type RemoteSecretCreator interface {
	GetRemoteSecret(context.Context, []byte, *kcmv1beta1.ClusterDeployment, CreateOptions) (*corev1.Secret, error)
}

type defaultRemoteSecretCreator struct{}

func newDefaultRemoteSecretCreator() RemoteSecretCreator {
	return &defaultRemoteSecretCreator{}
}

// GetRemoteSecret creates a remote secret for Istio using the provided kubeconfig.
func (rs *defaultRemoteSecretCreator) GetRemoteSecret(ctx context.Context, kubeconfig []byte, clusterDeployment *kcmv1beta1.ClusterDeployment, opt CreateOptions) (*corev1.Secret, error) {
	log := log.FromContext(ctx)

	kubeClient, err := k8s.NewKubeClientFromKubeconfig(kubeconfig)
	if err != nil {
		log.Error(err, "failed to create kube client from kubeconfig")
		return nil, err
	}

	secret, warn, err := CreateRemoteSecret(
		ctx,
		RemoteSecretOptions{
			AllowOverwrite: opt.AllowOverwrite,
			MultiCluster:   labels.IsKCMRegionCluster(clusterDeployment.Labels),
			Type:           SecretTypeRemote,
			AuthType:       RemoteSecretAuthTypeBearerToken,
			ClusterName:    clusterDeployment.Name,
			KubeOptions: KubeOptions{
				Namespace: istio.IstioSystemNamespace,
			},
		},
		clusterDeployment.Namespace,
		kubeClient)
	if err != nil {
		log.Error(err, "failed to create remote secret")
		return nil, err
	}

	if warn != nil {
		log.Info("warning when generating remote secret", "warning", warn)
	}

	return secret, nil
}
