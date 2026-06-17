package utils

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	kcmv1beta1 "github.com/K0rdent/kcm/api/v1beta1"
	"github.com/k0rdent/istio/istio-operator/internal/controller/record"
	"github.com/k0rdent/istio/istio-operator/internal/labels"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func GetEventsAnnotations(obj runtime.Object) map[string]string {
	var generation string

	metaObj, ok := obj.(metav1.Object)
	if !ok {
		metaObj = &metav1.ObjectMeta{}
	}

	if metaObj.GetGeneration() == 0 {
		generation = "nil"
	} else {
		generation = strconv.Itoa(int(metaObj.GetGeneration()))
	}

	return map[string]string{
		"generation": generation,
	}
}

func GetClusterDeploymentStub(name, namespace string) *kcmv1beta1.ClusterDeployment {
	return &kcmv1beta1.ClusterDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		TypeMeta: metav1.TypeMeta{
			APIVersion: kcmv1beta1.GroupVersion.String(),
			Kind:       kcmv1beta1.ClusterDeploymentKind,
		},
	}
}

// Creates a log line and an `Event` object from the same arguments.
//
// If you pass `nil` instead of `err`,
// then `log.Info` and `record.Event` are used,
// else `log.Error` and `record.Warn` are used.
//
// Example:
//
//	utils.LogEvent(
//		ctx,
//		"ConfigMapUpdateFailed",
//		"Failed to update ConfigMap",
//		clusterDeployment,
//		err,
//		"configMapName", configMap.Name,
//		"key2", "value2",
//		"key3", "value3",
//	)
func LogEvent(
	ctx context.Context,
	reason, message string,
	obj runtime.Object,
	err error,
	keysAndValues ...any,
) {
	log := log.FromContext(ctx)
	recordFunc := record.Event

	if err == nil {
		log.Info(message, keysAndValues...)
	} else {
		log.Error(err, message, keysAndValues...)
		recordFunc = record.Warn
		keysAndValues = append([]any{"err", err}, keysAndValues...)
	}

	parts := make([]string, 0, len(keysAndValues))
	for i, keyOrValue := range keysAndValues {
		if i%2 == 0 { // key
			parts = append(parts, fmt.Sprintf(", %v=", keyOrValue))
		} else { // value
			parts = append(parts, fmt.Sprintf("%#v", keyOrValue))
		}
	}

	recordFunc(
		obj,
		GetEventsAnnotations(obj),
		reason,
		message+strings.Join(parts, ""),
	)
}

func IsAdopted(cluster *kcmv1beta1.ClusterDeployment) bool {
	return strings.HasPrefix(cluster.Spec.Template, "adopted-")
}

func IsResourceExists(ctx context.Context, client client.Client, obj client.Object, name, namespace string) (bool, error) {
	if err := client.Get(ctx, types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, obj); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func IsInMesh(cd *kcmv1beta1.ClusterDeployment) bool {
	return labels.HasIstioMeshLabel(cd.Labels)
}

// MustPropagationServiceValuesYAML builds Helm values for propagation.yaml.
// Uses a block scalar for propagation.data.
func MustPropagationServiceValuesYAML(templateResourceIdentifier string) string {
	return fmt.Sprintf(`propagation:
  enabled: true
  data: |
{{- $secret := fromYaml (copy %q) }}
{{- if not $secret.metadata.labels }}
{{- $_ := set $secret.metadata "labels" (dict) }}
{{- end }}
{{- $_ := set $secret.metadata.labels "istio/multiCluster" "true" }}
{{ $secret | toYaml | nindent 14 }}
`, templateResourceIdentifier)
}

// MustScopedCAPropagationServiceValuesYAML builds Helm values for propagation.yaml.
// The target cluster hash is derived from the propagated source secret name suffix,
// so values do not need a separately injected hash literal.
func MustScopedCAPropagationServiceValuesYAML(templateResourceIdentifier, templateResourceName string) string {
	return fmt.Sprintf(`{{- $eligible := or (eq (adler32sum (printf "%%s-%%s" .Cluster.metadata.namespace .Cluster.metadata.name)) (last (splitList "-" %q))) (and .Cluster.metadata.labels (eq (index .Cluster.metadata.labels %q) "true")) }}
propagation:
  enabled: {{ $eligible }}
  data: |
{{ if $eligible }}
{{ copy %q | nindent 14 }}
{{ end }}
`, templateResourceName, labels.KCMRegionClusterLabel, templateResourceIdentifier)
}

// IsClusterDeploymentReady checks if a ClusterDeployment is considered ready.
// Due to a bug in KCM or its upstream dependency, where some conditions are always false,
// we cannot rely solely on the Ready condition.
// Instead, we check if a CAPIClusterSummaryCondition exists in the conditions list
// to determine if a kubeconfig has been created for the cluster.
func IsClusterDeploymentReady(cd *kcmv1beta1.ClusterDeployment) bool {
	for _, condition := range *cd.GetConditions() {
		if IsAdopted(cd) {
			if condition.Type == kcmv1beta1.ReadyCondition {
				return true
			}
		} else {
			if condition.Type == kcmv1beta1.CAPIClusterSummaryCondition {
				return true
			}
		}
	}

	return false
}

func IsResourceCreatedByOperator(obj metav1.Object) bool {
	v, ok := obj.GetLabels()[labels.ManagedByLabel]
	return ok && v == labels.ManagedByIstioOperator
}
