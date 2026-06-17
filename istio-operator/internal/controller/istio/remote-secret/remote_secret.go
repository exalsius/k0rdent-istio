// Copyright Istio Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package remotesecret

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/k0rdent/istio/istio-operator/internal/controller/istio"
	"github.com/k0rdent/istio/istio-operator/internal/hash"
	"github.com/k0rdent/istio/istio-operator/internal/k8s"
	"github.com/k0rdent/istio/istio-operator/internal/labels"
	"github.com/spf13/pflag"
	"istio.io/istio/pkg/config/constants"
	mcluster "istio.io/istio/pkg/kube/multicluster"
	authenticationv1 "k8s.io/api/authentication/v1"
	v1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/tools/clientcmd/api/latest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	remoteSecretPrefix = "istio-remote-secret"
	configSecretName   = "istio-kubeconfig"
	configSecretKey    = "config"

	clusterNameAnnotationKey = "networking.istio.io/cluster"
)

const (
	serviceAccountTokenExpirationSeconds = 60 * 120 // 2 hours
)

var (
	errMissingRootCAKey = fmt.Errorf("no %q data found", v1.ServiceAccountRootCAKey)
	errMissingTokenKey  = fmt.Errorf("no %q data found", v1.ServiceAccountTokenKey)

	tokenWaitBackoff = time.Second
)

type Warning error

// RemoteSecretAuthType is a strongly typed authentication type suitable for use with pflags.Var().
type (
	RemoteSecretAuthType string
	SecretType           string
)

var _ pflag.Value = (*RemoteSecretAuthType)(nil)

func (at *RemoteSecretAuthType) String() string { return string(*at) }
func (at *RemoteSecretAuthType) Type() string   { return "RemoteSecretAuthType" }
func (at *RemoteSecretAuthType) Set(in string) error {
	*at = RemoteSecretAuthType(in)
	return nil
}

func (at *SecretType) String() string { return string(*at) }
func (at *SecretType) Type() string   { return "SecretType" }
func (at *SecretType) Set(in string) error {
	*at = SecretType(in)
	return nil
}

const (
	// Use a bearer token for authentication to the remote kubernetes cluster.
	RemoteSecretAuthTypeBearerToken RemoteSecretAuthType = "bearer-token"

	// Use a custom authentication plugin for the remote kubernetes cluster.
	RemoteSecretAuthTypePlugin RemoteSecretAuthType = "plugin"

	// Secret generated from remote cluster
	SecretTypeRemote SecretType = "remote"

	// Secret generated from config cluster
	SecretTypeConfig SecretType = "config"
)

// KubeOptions contains kubernetes options common to all commands.
type KubeOptions struct {
	Kubeconfig string
	Context    string
	Namespace  string
}

// RemoteSecretOptions contains the options for creating a remote secret.
type RemoteSecretOptions struct {
	KubeOptions

	// MultiCluster controls the value of the label Istio watches for remote cluster discovery.
	MultiCluster bool

	// Name of the local cluster whose credentials are stored in the secret. Must be
	// DNS1123 label as it will be used for the k8s secret name.
	ClusterName string

	// Create a secret with this service account's credentials.
	ServiceAccountName string

	// Authentication method for the remote Kubernetes cluster.
	AuthType RemoteSecretAuthType
	// Authenticator plugin configuration
	AuthPluginName   string
	AuthPluginConfig map[string]string

	// Type of the generated secret
	Type SecretType

	// ManifestsPath is a path to a manifestsPath and profiles directory in the local filesystem,
	// or URL with a release tgz. This is only used when no reader service account exists and has
	// to be created.
	ManifestsPath string

	// ServerOverride overrides the server IP/hostname field from the Kubeconfig
	ServerOverride string

	// SecretName selects a specific secret from the remote service account, if there are multiple
	SecretName string

	// AllowOverwrite enforces creation of a new remote secret and service account token secret
	// even if they already exists.
	AllowOverwrite bool
}

func CreateRemoteSecret(ctx context.Context, opt RemoteSecretOptions, clusterNamespace string, client *k8s.KubeClient) (*v1.Secret, Warning, error) {
	// generate the clusterName if not specified
	if opt.ClusterName == "" {
		uid, err := clusterUID(client.Clientset)
		if err != nil {
			return nil, nil, err
		}
		opt.ClusterName = string(uid)
	}

	var secretName string
	switch opt.Type {
	case SecretTypeRemote:
		secretName = GetRemoteSecretName(opt.ClusterName, clusterNamespace)
		if opt.ServiceAccountName == "" {
			opt.ServiceAccountName = constants.DefaultServiceAccountName
		}
	case SecretTypeConfig:
		secretName = configSecretName
		if opt.ServiceAccountName == "" {
			opt.ServiceAccountName = constants.DefaultConfigServiceAccountName
		}
	default:
		return nil, nil, fmt.Errorf("unsupported type: %v", opt.Type)
	}
	tokenSecret, err := getServiceAccountSecret(client, opt, ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("could not get access token to read resources from local kube-apiserver: %v", err)
	}

	var server string
	var warn Warning
	if opt.ServerOverride != "" {
		server = opt.ServerOverride
	} else {
		server, warn, err = getServerFromKubeconfig(client)
		if err != nil {
			return nil, warn, err
		}
	}

	var remoteSecret *v1.Secret
	switch opt.AuthType {
	case RemoteSecretAuthTypeBearerToken:
		remoteSecret, err = createRemoteSecretFromTokenAndServer(client, tokenSecret, opt.ClusterName, server, secretName, opt.MultiCluster, ctx)
	case RemoteSecretAuthTypePlugin:
		authProviderConfig := &api.AuthProviderConfig{
			Name:   opt.AuthPluginName,
			Config: opt.AuthPluginConfig,
		}
		remoteSecret, err = createRemoteSecretFromPlugin(tokenSecret, server, opt.ClusterName, secretName, opt.MultiCluster,
			authProviderConfig)
	default:
		err = fmt.Errorf("unsupported authentication type: %v", opt.AuthType)
	}
	if err != nil {
		return nil, warn, err
	}

	remoteSecret.Namespace = opt.Namespace
	return remoteSecret, warn, nil
}

func createRemoteSecretFromPlugin(
	tokenSecret *v1.Secret,
	server, clusterName, secName string,
	multiCluster bool,
	authProviderConfig *api.AuthProviderConfig,
) (*v1.Secret, error) {
	caData, ok := tokenSecret.Data[v1.ServiceAccountRootCAKey]
	if !ok {
		return nil, errMissingRootCAKey
	}

	// Create a Kubeconfig to access the remote cluster using the auth provider plugin.
	kubeconfig := createPluginKubeconfig(caData, clusterName, server, authProviderConfig)
	if err := clientcmd.Validate(*kubeconfig); err != nil {
		return nil, fmt.Errorf("invalid kubeconfig: %v", err)
	}

	// Encode the Kubeconfig in a secret that can be loaded by Istio to dynamically discover and access the remote cluster.
	return createRemoteServiceAccountSecret(kubeconfig, clusterName, secName, multiCluster)
}

func createBaseKubeconfig(caData []byte, clusterName, server string) *api.Config {
	return &api.Config{
		Clusters: map[string]*api.Cluster{
			clusterName: {
				CertificateAuthorityData: caData,
				Server:                   server,
			},
		},
		AuthInfos: map[string]*api.AuthInfo{},
		Contexts: map[string]*api.Context{
			clusterName: {
				Cluster:  clusterName,
				AuthInfo: clusterName,
			},
		},
		CurrentContext: clusterName,
	}
}

func createRemoteServiceAccountSecret(kubeconfig *api.Config, clusterName, secName string, multiCluster bool) (*v1.Secret, error) { // nolint:interfacer
	var data bytes.Buffer
	if err := latest.Codec.Encode(kubeconfig, &data); err != nil {
		return nil, err
	}
	key := clusterName
	if secName == configSecretName {
		key = configSecretKey
	}
	out := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: secName,
			Annotations: map[string]string{
				clusterNameAnnotationKey: clusterName,
			},
			Labels: map[string]string{
				labels.K0rdentIstioVersionLabel:  istio.ReleaseVersion,
				labels.ManagedByLabel:            labels.ManagedByIstioOperator,
				mcluster.MultiClusterSecretLabel: fmt.Sprintf("%t", multiCluster),
			},
		},
		Data: map[string][]byte{
			key: data.Bytes(),
		},
	}
	return out, nil
}

func createPluginKubeconfig(caData []byte, clusterName, server string, authProviderConfig *api.AuthProviderConfig) *api.Config {
	c := createBaseKubeconfig(caData, clusterName, server)
	c.AuthInfos[c.CurrentContext] = &api.AuthInfo{
		AuthProvider: authProviderConfig,
	}
	return c
}

func createRemoteSecretFromTokenAndServer(client *k8s.KubeClient, tokenSecret *v1.Secret, clusterName, server, secName string, multiCluster bool, ctx context.Context) (*v1.Secret, error) {
	caData, token, err := waitForTokenData(client, tokenSecret, ctx)
	if err != nil {
		return nil, err
	}

	// Create a Kubeconfig to access the remote cluster using the remote service account credentials.
	kubeconfig := createBearerTokenKubeconfig(caData, token, clusterName, server)
	if err := clientcmd.Validate(*kubeconfig); err != nil {
		return nil, fmt.Errorf("invalid kubeconfig: %v", err)
	}

	// Encode the Kubeconfig in a secret that can be loaded by Istio to dynamically discover and access the remote cluster.
	return createRemoteServiceAccountSecret(kubeconfig, clusterName, secName, multiCluster)
}

func waitForTokenData(client *k8s.KubeClient, secret *v1.Secret, ctx context.Context) (ca, token []byte, err error) {
	log := log.FromContext(ctx)

	ca, token, err = tokenDataFromSecret(secret)
	if err == nil {
		return
	}

	log.Info("Waiting for data to be populated", "secret name", secret.Name)
	err = backoff.Retry(
		func() error {
			secret, err = client.Clientset.CoreV1().Secrets(secret.Namespace).Get(ctx, secret.Name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			ca, token, err = tokenDataFromSecret(secret)
			return err
		},
		backoff.WithMaxRetries(backoff.NewConstantBackOff(tokenWaitBackoff), 5))
	return
}

func tokenDataFromSecret(tokenSecret *v1.Secret) (ca, token []byte, err error) {
	var ok bool
	ca, ok = tokenSecret.Data[v1.ServiceAccountRootCAKey]
	if !ok {
		err = errMissingRootCAKey
		return
	}
	token, ok = tokenSecret.Data[v1.ServiceAccountTokenKey]
	if !ok {
		err = errMissingTokenKey
		return
	}
	return
}

func getServerFromKubeconfig(client *k8s.KubeClient) (string, Warning, error) {
	restCfg, err := client.Config.ClientConfig()
	if err != nil {
		return "", nil, fmt.Errorf("failed getting REST config from client: %v", err)
	}

	server := restCfg.Host
	if strings.Contains(server, "127.0.0.1") || strings.Contains(server, "localhost") {
		return server, fmt.Errorf(
			"server in Kubeconfig is %s. This is likely not reachable from inside the cluster, "+
				"if you're using Kubernetes in Docker, pass --server with the container IP for the API Server",
			server), nil
	}
	return server, nil, nil
}

func GetRemoteSecretName(clusterName, namespace string) string {
	name := fmt.Sprintf("%s-%s", namespace, clusterName)
	return hash.WithPrefix(remoteSecretPrefix, name, hash.AdlerHash)
}

func getServiceAccountSecret(client *k8s.KubeClient, opt RemoteSecretOptions, ctx context.Context) (*v1.Secret, error) {
	// Create the service account if it doesn't exist.
	serviceAccount, err := getOrCreateServiceAccount(client, opt)
	if err != nil {
		return nil, err
	}

	if !k8s.IsAtLeastVersion(client.Clientset, 24) {
		return legacyGetServiceAccountSecret(serviceAccount, client, opt)
	}
	return getOrCreateServiceAccountSecret(serviceAccount, client, opt, ctx)
}

func legacyGetServiceAccountSecret(
	serviceAccount *v1.ServiceAccount,
	client *k8s.KubeClient,
	opt RemoteSecretOptions,
) (*v1.Secret, error) {
	if len(serviceAccount.Secrets) == 0 {
		return nil, fmt.Errorf("no secret found in the service account: %s", serviceAccount)
	}

	secretName := ""
	secretNamespace := ""
	if opt.SecretName != "" {
		found := false
		for _, secret := range serviceAccount.Secrets {
			if secret.Name == opt.SecretName {
				found = true
				secretName = secret.Name
				secretNamespace = secret.Namespace
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("provided secret does not exist: %s", opt.SecretName)
		}
	} else {
		if len(serviceAccount.Secrets) == 1 {
			secretName = serviceAccount.Secrets[0].Name
			secretNamespace = serviceAccount.Secrets[0].Namespace
		} else {
			return nil, fmt.Errorf("wrong number of secrets (%v) in serviceaccount %s/%s, please use --secret-name to specify one",
				len(serviceAccount.Secrets), opt.Namespace, opt.ServiceAccountName)
		}
	}

	if secretNamespace == "" {
		secretNamespace = opt.Namespace
	}
	return client.Clientset.CoreV1().Secrets(secretNamespace).Get(context.TODO(), secretName, metav1.GetOptions{})
}

func getOrCreateServiceAccountSecret(
	serviceAccount *v1.ServiceAccount,
	client *k8s.KubeClient,
	opt RemoteSecretOptions,
	ctx context.Context,
) (*v1.Secret, error) {
	log := log.FromContext(ctx)

	secretName := opt.SecretName
	if secretName == "" {
		secretName = tokenSecretName(serviceAccount.Name)
	}

	existingSecret, err := client.Clientset.CoreV1().Secrets(opt.Namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil && !kerrors.IsNotFound(err) {
		return nil, fmt.Errorf("failed to get existing secret %s/%s: %w", opt.Namespace, secretName, err)
	}

	if !opt.AllowOverwrite && existingSecret.Name != "" {
		log.Info("Found existing service account secret", "secret", secretName, "namespace", opt.Namespace)
		return existingSecret, nil
	}

	caCert, err := getCAcert(client)
	if err != nil {
		return nil, fmt.Errorf("failed to get CA certificate: %w", err)
	}

	saToken, err := getServiceAccountToken(ctx, client, serviceAccount.Name, opt.Namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get service account token: %w", err)
	}
	log.Info("Service Account token was successfully generated", "serviceAccount", serviceAccount.Name, "namespace", opt.Namespace)

	if opt.AllowOverwrite && existingSecret.Name != "" {
		existingSecret.Type = v1.SecretTypeOpaque
		existingSecret.Data = map[string][]byte{
			"token":     []byte(saToken),
			"namespace": []byte(opt.Namespace),
			"ca.crt":    caCert,
		}
		existingSecret.Annotations = map[string]string{
			v1.ServiceAccountNameKey: serviceAccount.Name,
		}

		log.Info("Updating existing secret with new token", "secret", secretName, "namespace", opt.Namespace)
		updatedSecret, err := client.Clientset.CoreV1().Secrets(opt.Namespace).Update(ctx, existingSecret, metav1.UpdateOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to update existing secret %s/%s: %w", opt.Namespace, existingSecret.Name, err)
		}
		return updatedSecret, nil
	}

	secret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: opt.Namespace,
			Labels: map[string]string{
				labels.K0rdentIstioVersionLabel: istio.ReleaseVersion,
				labels.ManagedByLabel:           labels.ManagedByIstioOperator,
			},
			Annotations: map[string]string{v1.ServiceAccountNameKey: serviceAccount.Name},
		},
		Type: v1.SecretTypeOpaque,
		Data: map[string][]byte{
			"token":     []byte(saToken),
			"namespace": []byte(opt.Namespace),
			"ca.crt":    caCert,
		},
	}

	log.Info("Creating new secret with token", "secret", secretName, "namespace", opt.Namespace)
	return client.Clientset.CoreV1().Secrets(opt.Namespace).Create(ctx, secret, metav1.CreateOptions{})
}

func getOrCreateServiceAccount(client *k8s.KubeClient, opt RemoteSecretOptions) (*v1.ServiceAccount, error) {
	sa, err := client.Clientset.CoreV1().ServiceAccounts(opt.Namespace).Get(context.TODO(), opt.ServiceAccountName, metav1.GetOptions{})
	if err != nil {
		if kerrors.IsNotFound(err) {
			return nil, errors.New("service account not found, it should be created by K0rdent Istio helm chart")
		}
		return nil, fmt.Errorf("failed to get ServiceAccount: %v", err)
	}
	return sa, nil
}

func tokenSecretName(saName string) string {
	return saName + "-istio-remote-secret-token"
}

func createBearerTokenKubeconfig(caData, token []byte, clusterName, server string) *api.Config {
	c := createBaseKubeconfig(caData, clusterName, server)
	c.AuthInfos[c.CurrentContext] = &api.AuthInfo{
		Token: string(token),
	}
	return c
}

func clusterUID(client kubernetes.Interface) (types.UID, error) {
	kubeSystem, err := client.CoreV1().Namespaces().Get(context.TODO(), "kube-system", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	return kubeSystem.UID, nil
}

func getCAcert(client *k8s.KubeClient) ([]byte, error) {
	rawConfig, err := client.Config.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get raw config: %w", err)
	}

	currentCtx := rawConfig.CurrentContext
	ctx := rawConfig.Contexts[currentCtx]
	if ctx == nil {
		// No current context, likely running in-cluster. Fall back to the REST config CA.
		return getCAcertFromRestConfig(client)
	}

	cluster := rawConfig.Clusters[ctx.Cluster]
	if cluster == nil {
		return nil, fmt.Errorf("no cluster info for %q", ctx.Cluster)
	}

	if len(cluster.CertificateAuthorityData) > 0 {
		return cluster.CertificateAuthorityData, nil
	}

	if cluster.CertificateAuthority != "" {
		return os.ReadFile(cluster.CertificateAuthority)
	}

	return nil, fmt.Errorf("no CA certificate found for cluster %q", ctx.Cluster)
}

func getCAcertFromRestConfig(client *k8s.KubeClient) ([]byte, error) {
	restCfg, err := client.Config.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get REST config: %w", err)
	}

	if len(restCfg.CAData) > 0 {
		return restCfg.CAData, nil
	}

	if restCfg.CAFile != "" {
		return os.ReadFile(restCfg.CAFile)
	}

	return nil, fmt.Errorf("no CA certificate found in REST config")
}

func getServiceAccountToken(ctx context.Context, client *k8s.KubeClient, saName, namespace string) (string, error) {
	tokenReq := &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			Audiences:         []string{},
			ExpirationSeconds: ptr.To(int64(serviceAccountTokenExpirationSeconds)),
		},
	}

	tokenResp, err := client.Clientset.CoreV1().ServiceAccounts(namespace).CreateToken(ctx, saName, tokenReq, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to create token for service account %s/%s: %w", namespace, saName, err)
	}

	return tokenResp.Status.Token, nil
}
