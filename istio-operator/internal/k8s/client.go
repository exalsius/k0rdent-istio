package k8s

import (
	"context"
	"fmt"

	kcmv1beta1 "github.com/K0rdent/kcm/api/v1beta1"
	cmv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	crds "github.com/k0rdent/istio/istio-operator/internal/crd"
	sveltosv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var LocalKubeClient *KubeClient
var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(crds.AddToScheme(scheme))
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(cmv1.AddToScheme(scheme))
	utilruntime.Must(sveltosv1beta1.AddToScheme(scheme))
}

type KubeClient struct {
	Client    client.Client
	Config    clientcmd.ClientConfig
	Clientset *kubernetes.Clientset
}

func NewClient() (*KubeClient, error) {
	config := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{},
	)

	return newKubeClient(config)
}

func NewKubeClientFromKubeconfig(kubeconfig []byte) (*KubeClient, error) {
	config, err := clientcmd.NewClientConfigFromBytes(kubeconfig)
	if err != nil {
		return nil, err
	}

	return newKubeClient(config)
}

// KubeconfigSecretNamespaceFallback is the ordered list of namespaces to try when
// resolving a kubeconfig secret: ClusterDeployment namespace, then kcm-system, then istio-system.
var KubeconfigSecretNamespaceFallback = []string{DefaultKCMSystemNamespace, DefaultIstioSystemNamespace}

func GetKubeconfigFromClusterDeployment(ctx context.Context, client client.Client, cd *kcmv1beta1.ClusterDeployment) ([]byte, error) {
	kubeconfigSecretName, err := GetKubeconfigSecretName(ctx, client, cd)
	if err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig secret name: %v", err)
	}

	namespaces := make([]string, 0, 1+len(KubeconfigSecretNamespaceFallback))
	namespaces = append(namespaces, cd.Namespace)
	namespaces = append(namespaces, KubeconfigSecretNamespaceFallback...)
	return GetKubeconfigFromSecretInNamespaces(ctx, client, kubeconfigSecretName, namespaces)
}

// GetKubeconfigFromSecretInNamespaces returns kubeconfig data from the named secret,
// trying each namespace in order. Returns the first successful result.
func GetKubeconfigFromSecretInNamespaces(ctx context.Context, client client.Client, secretName string, namespaces []string) ([]byte, error) {
	for _, ns := range namespaces {
		secret, err := GetSecret(ctx, client, secretName, ns)
		if err != nil {
			continue
		}
		kubeconfig := GetSecretValue(secret)
		if kubeconfig != nil {
			return kubeconfig, nil
		}
	}
	return nil, fmt.Errorf("failed to get secret %q in any of %v", secretName, namespaces)
}

func GetKubeconfigFromSecret(ctx context.Context, client client.Client, secretName string) ([]byte, error) {
	return GetKubeconfigFromSecretInNamespaces(ctx, client, secretName, KubeconfigSecretNamespaceFallback)
}

func NewKubeClientFromSecret(ctx context.Context, client client.Client, secretName, namespace string) (*KubeClient, error) {
	secret, err := GetSecret(ctx, client, secretName, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get secret: %v", err)
	}

	kubeconfig := GetSecretValue(secret)
	if kubeconfig == nil {
		return nil, fmt.Errorf("kubeconfig is empty")
	}

	kubeClient, err := NewKubeClientFromKubeconfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create new client from kubeconfig: %v", err)
	}

	return kubeClient, nil
}

func newKubeClient(config clientcmd.ClientConfig) (*KubeClient, error) {
	restConfig, err := config.ClientConfig()
	if err != nil {
		return nil, err
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}

	client, err := client.New(restConfig, client.Options{
		Scheme: scheme,
	})
	if err != nil {
		return nil, err
	}

	return &KubeClient{
		Client:    client,
		Clientset: clientset,
		Config:    config,
	}, nil
}
