package remotesecret

import (
	"context"
	"fmt"

	kcmv1beta1 "github.com/K0rdent/kcm/api/v1beta1"
	"github.com/k0rdent/istio/istio-operator/internal/controller/istio"
	"github.com/k0rdent/istio/istio-operator/internal/controller/record"
	"github.com/k0rdent/istio/istio-operator/internal/controller/utils"
	"github.com/k0rdent/istio/istio-operator/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	client client.Client
	IIstioRemoteSecretCreator
}

func New(c client.Client) *RemoteSecretManager {
	return &RemoteSecretManager{
		client:                    c,
		IIstioRemoteSecretCreator: NewIstioRemoteSecret(),
	}
}

// Function tries to delete the remote secret
func (rs *RemoteSecretManager) TryDelete(ctx context.Context, request ctrl.Request) error {
	log := log.FromContext(ctx)
	log.Info("Trying to delete remote secret")

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

// Function handles the creation of a remote secret
func (rs *RemoteSecretManager) TryCreate(ctx context.Context, clusterDeployment *kcmv1beta1.ClusterDeployment, opt CreateOptions) error {
	log := log.FromContext(ctx)
	log.Info("Trying to create remote secret")

	if !utils.IsClusterDeploymentReady(clusterDeployment) {
		log.Info("Cluster deployment is not ready")
		return nil
	}

	if !opt.AllowOverwrite {
		exists, err := rs.remoteSecretExists(ctx, clusterDeployment)
		if err != nil {
			return fmt.Errorf("failed to check remote secret: %v", err)
		}

		if exists {
			log.Info("Remote secret already exists")
			return nil
		}
	}

	createdInKCMRegion, err := k8s.CreatedInKCMRegion(ctx, rs.client, clusterDeployment)
	if err != nil {
		return fmt.Errorf("failed to determine cluster region: %v", err)
	}

	regionKubeClient := rs.client

	if createdInKCMRegion {
		regionClusterName, err := k8s.GetKcmRegionClusterNameRelatedToClusterDeployment(ctx, rs.client, clusterDeployment)
		if err != nil {
			return fmt.Errorf("failed to get cluster region: %v", err)
		}

		regionKubeconfig, err := k8s.GetKubeconfigByRegionName(ctx, rs.client, regionClusterName)
		if err != nil {
			return fmt.Errorf("failed to get kubeconfig by region name: %v", err)
		}

		if regionKubeconfig == nil {
			return fmt.Errorf("no kubeconfig found for region cluster name: %s", regionClusterName)
		}

		regionClient, err := k8s.NewKubeClientFromKubeconfig(regionKubeconfig)
		if err != nil {
			return fmt.Errorf("failed to create kube client from region kubeconfig: %v", err)
		}

		regionKubeClient = regionClient.Client
	}

	kubeconfigSecretName, err := k8s.GetKubeconfigSecretName(ctx, rs.client, clusterDeployment)
	if err != nil {
		return fmt.Errorf("failed to get kubeconfig secret name: %v", err)
	}

	namespaces := make([]string, 0, 1+len(k8s.KubeconfigSecretNamespaceFallback))
	namespaces = append(namespaces, clusterDeployment.Namespace)
	namespaces = append(namespaces, k8s.KubeconfigSecretNamespaceFallback...)
	kubeconfig, err := k8s.GetKubeconfigFromSecretInNamespaces(ctx, regionKubeClient, kubeconfigSecretName, namespaces)
	if err != nil {
		return fmt.Errorf("failed to get kubeconfig from secret: %v", err)
	}

	remoteSecret, err := rs.GetRemoteSecret(ctx, kubeconfig, clusterDeployment, opt)
	if err != nil {
		return fmt.Errorf("failed to get remote secret: %v", err)
	}

	if err := rs.createSecretResource(ctx, remoteSecret); err != nil {
		log.Error(err, "failed to create remote secret")
		return fmt.Errorf("failed to create remote secret: %v", err)
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

// Function creates the remote secret resource in k8s
func (rs *RemoteSecretManager) createSecretResource(ctx context.Context, secret *corev1.Secret) error {
	if err := rs.client.Delete(ctx, secret); err != nil && !errors.IsNotFound(err) {
		return err
	}

	if err := rs.client.Create(ctx, secret); err != nil {
		return err
	}

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

type IstioRemoteSecretCreator struct{}

type IIstioRemoteSecretCreator interface {
	GetRemoteSecret(context.Context, []byte, *kcmv1beta1.ClusterDeployment, CreateOptions) (*corev1.Secret, error)
}

func NewIstioRemoteSecret() IIstioRemoteSecretCreator {
	return &IstioRemoteSecretCreator{}
}

// Function creates a remote secret for Istio using the provided kubeconfig
func (rs *IstioRemoteSecretCreator) GetRemoteSecret(ctx context.Context, kubeconfig []byte, clusterDeployment *kcmv1beta1.ClusterDeployment, opt CreateOptions) (*corev1.Secret, error) {
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
