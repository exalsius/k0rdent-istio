package utils

import (
	"strings"
	"testing"
)

func TestMustScopedCAPropagationServiceValuesYAML_BaseStructure(t *testing.T) {
	values := MustScopedCAPropagationServiceValuesYAML("CASecretData", "istio-ca-secret-2484144794")

	assertContainsAll(t, values,
		`{{- $eligible := or (eq (adler32sum (printf "%s-%s" .Cluster.metadata.namespace .Cluster.metadata.name)) (last (splitList "-" "istio-ca-secret-2484144794"))) (and .Cluster.metadata.labels (eq (index .Cluster.metadata.labels "k0rdent.mirantis.com/kcm-region-cluster") "true")) }}`,
		`enabled: {{ $eligible }}`,
		`data: |`,
		`{{ if $eligible }}`,
		`{{ copy "CASecretData" | nindent 14 }}`,
		`{{ end }}`,
	)
}

func TestMustScopedCAPropagationServiceValuesYAML_DifferentInputs(t *testing.T) {
	tests := []struct {
		name       string
		identifier string
		secretName string
	}{
		{name: "primary hash", identifier: "CASecretData", secretName: "istio-ca-secret-2484144794"},
		{name: "secondary hash", identifier: "SecretPayload", secretName: "istio-ca-secret-1946159437"},
		{name: "short hash", identifier: "Data", secretName: "x-42"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := MustScopedCAPropagationServiceValuesYAML(tt.identifier, tt.secretName)
			assertContainsAll(t, values,
				`{{- $eligible := or (eq (adler32sum (printf "%s-%s" .Cluster.metadata.namespace .Cluster.metadata.name)) (last (splitList "-" "`+tt.secretName+`")))`,
				`enabled: {{ $eligible }}`,
				`{{ if $eligible }}`,
				`index .Cluster.metadata.labels "k0rdent.mirantis.com/kcm-region-cluster"`,
				`{{ copy "`+tt.identifier+`" | nindent 14 }}`,
			)
		})
	}
}

func TestMustScopedCAPropagationServiceValuesYAML_UsesBlockConditionals(t *testing.T) {
	values := MustScopedCAPropagationServiceValuesYAML("CASecretData", "istio-ca-secret-2484144794")
	for _, expected := range []string{"{{ if", "{{ end }}"} {
		if !strings.Contains(values, expected) {
			t.Fatalf("expected values to contain %q, got:\n%s", expected, values)
		}
	}
}

func TestMustPropagationServiceValuesYAML_OverridesMultiClusterLabel(t *testing.T) {
	values := MustPropagationServiceValuesYAML("RemoteSecretData")

	assertContainsAll(t, values,
		`{{- $secret := fromYaml (copy "RemoteSecretData") }}`,
		`{{- if not $secret.metadata.labels }}`,
		`{{- $_ := set $secret.metadata "labels" (dict) }}`,
		`{{- $_ := set $secret.metadata.labels "istio/multiCluster" "true" }}`,
		`{{ $secret | toYaml | nindent 14 }}`,
	)
}

func assertContainsAll(t *testing.T, values string, checks ...string) {
	t.Helper()

	for _, check := range checks {
		if !strings.Contains(values, check) {
			t.Fatalf("expected values to contain %q, got:\n%s", check, values)
		}
	}
}
