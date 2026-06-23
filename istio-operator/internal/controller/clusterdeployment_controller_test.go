package controller

import (
	"context"
	"fmt"
	"time"

	kcmv1beta1 "github.com/K0rdent/kcm/api/v1beta1"
	cmv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	"github.com/k0rdent/istio/istio-operator/internal/controller/istio"
	"github.com/k0rdent/istio/istio-operator/internal/controller/istio/cert"
	"github.com/k0rdent/istio/istio-operator/internal/controller/istio/multicluster"
	remotesecret "github.com/k0rdent/istio/istio-operator/internal/controller/istio/remote-secret"
	"github.com/k0rdent/istio/istio-operator/internal/k8s"
	"github.com/k0rdent/istio/istio-operator/internal/labels"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const secretName = "test-istio-cd-kubeconfig"
const clusterDeploymentName = "test-istio-cd"

var _ = Describe("ClusterDeployment Controller", func() {
	Context("When reconciling a resource", func() {
		ctx := context.Background()

		istio.IstioSystemNamespace = "istio-system"
		istio.IstioReleaseName = "test-istio"

		remoteSecretNamespacedName := types.NamespacedName{
			Name:      remotesecret.GetRemoteSecretName(clusterDeploymentName, DefaultNamespace),
			Namespace: istio.IstioSystemNamespace,
		}

		clusterCertificateNamespacedName := types.NamespacedName{
			Name:      cert.GetCertName(clusterDeploymentName, DefaultNamespace),
			Namespace: istio.IstioSystemNamespace,
		}

		clusterDeploymentReconciler := &ClusterDeploymentReconciler{}

		clusterDeploymentNamespacedName := types.NamespacedName{
			Name:      clusterDeploymentName,
			Namespace: DefaultNamespace,
		}

		clusterDeploymentLabels := map[string]string{
			labels.IstioRoleLabel: labels.IstioRoleLabelMemberValue,
		}

		clusterDeploymentAnnotations := map[string]string{}

		createClusterDeployment := func(
			name string,
			namespace string,
			labels map[string]string,
			annotations map[string]string,
		) {
			clusterDeployment := &kcmv1beta1.ClusterDeployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:        name,
					Namespace:   namespace,
					Labels:      labels,
					Annotations: annotations,
				},
				Spec: kcmv1beta1.ClusterDeploymentSpec{
					Template:   "aws-cluster-template",
					Credential: name,
				},
			}
			Expect(k8sClient.Create(ctx, clusterDeployment)).To(Succeed())

			clusterDeployment.Status = kcmv1beta1.ClusterDeploymentStatus{
				Conditions: []metav1.Condition{
					{
						Type:               kcmv1beta1.ReadyCondition,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Time{Time: time.Now()},
						Reason:             "ClusterReady",
						Message:            "Cluster is ready",
					},
					{
						Type:               kcmv1beta1.CAPIClusterSummaryCondition,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Time{Time: time.Now()},
						Reason:             "InfrastructureReady",
						Message:            "Infrastructure is ready",
					},
				},
			}
			Expect(k8sClient.Status().Update(ctx, clusterDeployment)).To(Succeed())
		}

		createSecret := func(secretName, namespace string) {
			kubeconfigSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: namespace,
					Labels:    map[string]string{},
				},
				Data: map[string][]byte{"value": []byte("")},
			}
			Expect(k8sClient.Create(ctx, kubeconfigSecret)).To(Succeed())
		}

		createCred := func(credName, secretName, namespace string) {
			cred := &kcmv1beta1.Credential{
				ObjectMeta: metav1.ObjectMeta{
					Name:      credName,
					Namespace: namespace,
				},
				Spec: kcmv1beta1.CredentialSpec{
					IdentityRef: &corev1.ObjectReference{
						Name:      secretName,
						Namespace: namespace,
					},
				},
			}
			Expect(k8sClient.Create(ctx, cred)).To(Succeed())
		}

		BeforeEach(func() {
			clusterDeploymentReconciler = &ClusterDeploymentReconciler{
				Client:                         k8sClient,
				Scheme:                         k8sClient.Scheme(),
				RemoteSecretManager:            remotesecret.NewFakeManager(k8sClient),
				IstioCertManager:               cert.New(k8sClient),
				RemoteSecretPropagationManager: multicluster.New(k8sClient),
				RegionalWaypointManager:        multicluster.NewRegionalWaypointManager(k8sClient),
			}

			By(fmt.Sprintf("creating the %s namespace", istio.IstioSystemNamespace))
			certNamespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: istio.IstioSystemNamespace,
				},
			}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      istio.IstioSystemNamespace,
				Namespace: istio.IstioSystemNamespace,
			}, certNamespace)
			if err != nil && errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, certNamespace)).To(Succeed())
			}

			By("creating ClusterDeployment")
			createClusterDeployment(
				clusterDeploymentName,
				DefaultNamespace,
				clusterDeploymentLabels,
				clusterDeploymentAnnotations,
			)

			By("creating the fake Secret for the cluster deployment kubeconfig")
			createSecret(secretName, DefaultNamespace)
			createCred(
				clusterDeploymentName,
				secretName,
				DefaultNamespace,
			)

			createSecret(
				fmt.Sprintf("%s-%s", clusterDeploymentName, "kubeconfig"),
				k8s.DefaultKCMSystemNamespace,
			)
			createCred(
				clusterDeploymentName,
				fmt.Sprintf("%s-%s", clusterDeploymentName, "kubeconfig"),
				k8s.DefaultKCMSystemNamespace,
			)
		})

		// Test cases

		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			clusterDeployment := &kcmv1beta1.ClusterDeployment{}
			err := k8sClient.Get(ctx, clusterDeploymentNamespacedName, clusterDeployment)
			Expect(err).NotTo(HaveOccurred())

			_, err = clusterDeploymentReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: clusterDeploymentNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			remoteSecret := &corev1.Secret{}
			err = k8sClient.Get(ctx, remoteSecretNamespacedName, remoteSecret)
			Expect(err).NotTo(HaveOccurred())

			cert := &cmv1.Certificate{}
			err = k8sClient.Get(ctx, clusterCertificateNamespacedName, cert)
			Expect(err).NotTo(HaveOccurred())
			Expect(cert.Spec.CommonName).To(Equal(fmt.Sprintf("%s CA", clusterDeploymentName)))
		})

		It("should successfully reconcile the resource when deleted", func() {
			By("Reconciling the deleted resource")
			clusterDeployment := &kcmv1beta1.ClusterDeployment{}
			err := k8sClient.Get(ctx, clusterDeploymentNamespacedName, clusterDeployment)
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Delete(ctx, clusterDeployment)).To(Succeed())

			secret := &corev1.Secret{}
			err = k8sClient.Get(ctx, clusterDeploymentNamespacedName, secret)
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})

		It("should successfully reconcile the resource when not ready", func() {
			By("Reconciling the not ready resource")
			clusterDeployment := &kcmv1beta1.ClusterDeployment{}
			err := k8sClient.Get(ctx, clusterDeploymentNamespacedName, clusterDeployment)
			Expect(err).NotTo(HaveOccurred())

			for i := range clusterDeployment.Status.Conditions {
				if clusterDeployment.Status.Conditions[i].Type == kcmv1beta1.ReadyCondition {
					clusterDeployment.Status.Conditions[i].Status = metav1.ConditionFalse
					break
				}
			}

			err = k8sClient.Status().Update(ctx, clusterDeployment)
			Expect(err).NotTo(HaveOccurred())

			_, err = clusterDeploymentReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: clusterDeploymentNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			secret := &corev1.Secret{}
			err = k8sClient.Get(ctx, clusterDeploymentNamespacedName, secret)
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})

		It("should successfully reconcile when remote secret already exists", func() {
			By("Reconciling the resource with existed remote secret")
			clusterDeployment := &kcmv1beta1.ClusterDeployment{}
			err := k8sClient.Get(ctx, clusterDeploymentNamespacedName, clusterDeployment)
			Expect(err).NotTo(HaveOccurred())

			_, err = clusterDeploymentReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: clusterDeploymentNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			_, err = clusterDeploymentReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: clusterDeploymentNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			secret := &corev1.Secret{}
			err = k8sClient.Get(ctx, remoteSecretNamespacedName, secret)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should successfully reconcile after creating and deleting resource", func() {
			By("Verifying resource reconciliation after creation and deletion")
			cd := &kcmv1beta1.ClusterDeployment{}
			err := k8sClient.Get(ctx, clusterDeploymentNamespacedName, cd)
			Expect(err).NotTo(HaveOccurred())

			_, err = clusterDeploymentReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: clusterDeploymentNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Delete(ctx, cd)).To(Succeed())

			_, err = clusterDeploymentReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: clusterDeploymentNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			secret := &corev1.Secret{}
			err = k8sClient.Get(ctx, remoteSecretNamespacedName, secret)
			Expect(errors.IsNotFound(err)).To(BeTrue())

			cert := &cmv1.Certificate{}
			err = k8sClient.Get(ctx, clusterCertificateNamespacedName, cert)
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})
	})
})
