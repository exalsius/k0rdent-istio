package k8s

import (
	"context"
	"fmt"

	kcmv1beta1 "github.com/K0rdent/kcm/api/v1beta1"
	crds "github.com/k0rdent/istio/istio-operator/internal/crd"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// credentialNamespaceFallback is the ordered list of namespaces to try when
// resolving a Credential: ClusterDeployment namespace first, then kcm-system, then istio-system.
var credentialNamespaceFallback = []string{DefaultKCMSystemNamespace, DefaultIstioSystemNamespace}

// GetCredentialForClusterDeployment returns the Credential referenced by the ClusterDeployment,
// trying the ClusterDeployment's namespace first, then kcm-system, then istio-system.
func GetCredentialForClusterDeployment(ctx context.Context, c client.Client, cd *kcmv1beta1.ClusterDeployment) (*crds.Credential, error) {
	namespaces := make([]string, 0, 1+len(credentialNamespaceFallback))
	namespaces = append(namespaces, cd.Namespace)
	namespaces = append(namespaces, credentialNamespaceFallback...)

	for _, ns := range namespaces {
		cred := new(crds.Credential)
		nn := types.NamespacedName{Name: cd.Spec.Credential, Namespace: ns}
		if err := c.Get(ctx, nn, cred); err != nil {
			continue
		}
		return cred, nil
	}
	return nil, fmt.Errorf("Credential.%s %q not found", crds.GroupVersion.Group, cd.Spec.Credential)
}

// If a Credential has a non-empty region field, we assume the cluster was created in that KCM region
func CreatedInKCMRegion(ctx context.Context, client client.Client, cd *kcmv1beta1.ClusterDeployment) (bool, error) {
	cred, err := GetCredentialForClusterDeployment(ctx, client, cd)
	if err != nil {
		return false, err
	}

	if cred.Spec.Region != "" {
		return true, nil
	}

	return false, nil
}

func GetKcmRegionClusterNameRelatedToClusterDeployment(ctx context.Context, client client.Client, cd *kcmv1beta1.ClusterDeployment) (string, error) {
	cred, err := GetCredentialForClusterDeployment(ctx, client, cd)
	if err != nil {
		return "", err
	}
	return cred.Spec.Region, nil
}

func GetKubeconfigByRegionName(ctx context.Context, client client.Client, regionName string) ([]byte, error) {
	if regionName == "" {
		return nil, fmt.Errorf("region name is empty")
	}

	region := new(crds.Region)
	if err := client.Get(ctx, types.NamespacedName{Name: regionName}, region); err != nil {
		return nil, fmt.Errorf("failed to get Region %s: %v", regionName, err)
	}

	if region.Spec.ClusterDeployment != nil {
		cd := new(kcmv1beta1.ClusterDeployment)
		if err := client.Get(ctx, types.NamespacedName{
			Name:      region.Spec.ClusterDeployment.Name,
			Namespace: region.Spec.ClusterDeployment.Namespace,
		}, cd); err != nil {
			return nil, fmt.Errorf("failed to get ClusterDeployment %s: %v", region.Spec.ClusterDeployment.Name, err)
		}

		kubeconfig, err := GetKubeconfigFromClusterDeployment(ctx, client, cd)
		if err != nil {
			return nil, fmt.Errorf("failed to get kubeconfig from ClusterDeployment: %v", err)
		}
		return kubeconfig, nil
	}

	if region.Spec.KubeConfig != nil {
		return GetKubeconfigFromSecret(ctx, client, region.Spec.KubeConfig.Name)
	}

	return nil, nil
}
