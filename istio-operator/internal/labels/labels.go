package labels

const (
	// IstioMeshLabel is used to label all resources that belong to the same mesh, so that they can be easily selected.
	IstioMeshLabel = "k0rdent.mirantis.com/istio-mesh"
	// K0rdentIstioVersionLabel is used to label all resources with the k0rdent-Istio release version they are associated with.
	K0rdentIstioVersionLabel = "k0rdent.mirantis.com/istio-release-version"

	// IstioRoleLabel is used on ClusterDeployment objects to indicate that the cluster is using Istio.
	IstioRoleLabel = "k0rdent.mirantis.com/istio-role"
	// IstioRoleLabelMemberValue is the value of the `k0rdent.mirantis.com/istio-role` label for clusters that are part of an Istio mesh.
	IstioRoleLabelMemberValue = "member"
	// IstioRoleLabelChildValue is a deprecated legacy value of the `k0rdent.mirantis.com/istio-role` label for clusters that are part of an Istio mesh.
	// It is still supported for backward compatibility with existing clusters; new clusters should use `member`.
	IstioRoleLabelChildValue = "child"

	// ManagedByLabel is used to label all resources managed by the istio-operator, so that they can be easily selected.
	ManagedByLabel = "app.kubernetes.io/managed-by"
	// ManagedByIstioOperator is the value of the `app.kubernetes.io/managed-by` label for all resources managed by the istio-operator.
	ManagedByIstioOperator = "istio-operator"

	// ClusterNameLabel is used to label resources with the name of the cluster they belong to.
	ClusterNameLabel = "cluster-name"
	// ClusterNamespaceLabel is used to label resources with the namespace of the cluster they belong to.
	ClusterNamespaceLabel = "cluster-namespace"
	// KCMRegionClusterLabel marks clusters that belong to a KCM region.
	KCMRegionClusterLabel = "k0rdent.mirantis.com/kcm-region-cluster"
)

func HasIstioMeshLabel(labels map[string]string) bool {
	_, ok := labels[IstioMeshLabel]
	return ok
}

func HasIstioRoleLabel(labels map[string]string) bool {
	_, ok := labels[IstioRoleLabel]
	return ok
}

func IsKCMRegionCluster(labels map[string]string) bool {
	return labels[KCMRegionClusterLabel] == "true"
}

// IstioVersion returns the value of the `k0rdent.mirantis.com/istio-release-version` label, or an empty string if the label is not present.
func IstioVersion(labels map[string]string) string {
	return labels[K0rdentIstioVersionLabel]
}
