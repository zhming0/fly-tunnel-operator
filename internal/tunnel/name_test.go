package tunnel

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSanitizeFlyName(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantMax   int
		wantExact string // empty means just check constraints
	}{
		{
			name:      "already valid short name",
			input:     "fly-tunnel-default-nginx",
			wantExact: "fly-tunnel-default-nginx",
		},
		{
			name:      "uppercase converted to lowercase",
			input:     "Fly-Tunnel-Default-Nginx",
			wantExact: "fly-tunnel-default-nginx",
		},
		{
			name:      "underscores replaced with dashes",
			input:     "fly_tunnel_default_nginx",
			wantExact: "fly-tunnel-default-nginx",
		},
		{
			name:      "consecutive dashes collapsed",
			input:     "fly--tunnel---default-nginx",
			wantExact: "fly-tunnel-default-nginx",
		},
		{
			name:      "leading and trailing dashes trimmed",
			input:     "-fly-tunnel-default-nginx-",
			wantExact: "fly-tunnel-default-nginx",
		},
		{
			name:    "long name is truncated with hash",
			input:   "fly-tunnel-very-long-namespace-name-that-exceeds-the-sixty-three-character-limit-for-fly-io-apps",
			wantMax: flyNameMaxLen,
		},
		{
			name:      "dots replaced with dashes",
			input:     "fly-tunnel-my.namespace-my.service",
			wantExact: "fly-tunnel-my-namespace-my-service",
		},
		{
			name:      "empty after sanitization returns empty",
			input:     "...",
			wantExact: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeFlyName(tt.input)

			if tt.wantExact != "" && got != tt.wantExact {
				t.Errorf("sanitizeFlyName(%q) = %q, want %q", tt.input, got, tt.wantExact)
			}

			if len(got) > flyNameMaxLen {
				t.Errorf("sanitizeFlyName(%q) length = %d, exceeds max %d", tt.input, len(got), flyNameMaxLen)
			}

			if tt.wantMax > 0 && len(got) > tt.wantMax {
				t.Errorf("sanitizeFlyName(%q) length = %d, want max %d", tt.input, len(got), tt.wantMax)
			}

			// Verify only valid characters.
			for _, c := range got {
				if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
					t.Errorf("sanitizeFlyName(%q) contains invalid char %q", tt.input, string(c))
				}
			}

			// Verify no leading/trailing dashes.
			if got != "" {
				if strings.HasPrefix(got, "-") || strings.HasSuffix(got, "-") {
					t.Errorf("sanitizeFlyName(%q) = %q has leading/trailing dash", tt.input, got)
				}
			}

			// Verify no consecutive dashes.
			if strings.Contains(got, "--") {
				t.Errorf("sanitizeFlyName(%q) = %q contains consecutive dashes", tt.input, got)
			}
		})
	}
}

func TestSanitizeFlyName_TruncationPreservesUniqueness(t *testing.T) {
	// Two different long names that share the same prefix should produce different results.
	name1 := "fly-tunnel-" + strings.Repeat("a", 60) + "-service-one"
	name2 := "fly-tunnel-" + strings.Repeat("a", 60) + "-service-two"

	result1 := sanitizeFlyName(name1)
	result2 := sanitizeFlyName(name2)

	if result1 == result2 {
		t.Errorf("truncation lost uniqueness: both produced %q", result1)
	}

	if len(result1) > flyNameMaxLen || len(result2) > flyNameMaxLen {
		t.Errorf("results exceed max length: %d, %d", len(result1), len(result2))
	}
}

func TestFlyAppNameForService(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		svcName   string
		wantExact string // empty means just check constraints
	}{
		{
			name:      "normal short names",
			namespace: "default",
			svcName:   "nginx",
			wantExact: "fly-tunnel-default-nginx",
		},
		{
			name:      "typical gateway service",
			namespace: "envoy-gateway-system",
			svcName:   "envoy-gateway",
			wantExact: "fly-tunnel-envoy-gateway-system-envoy-gateway",
		},
		{
			name:      "very long namespace and service",
			namespace: "this-is-a-really-long-namespace-name",
			svcName:   "and-this-is-a-really-long-service-name-too",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      tt.svcName,
					Namespace: tt.namespace,
				},
			}

			got := flyAppNameForService(svc)

			if tt.wantExact != "" && got != tt.wantExact {
				t.Errorf("flyAppNameForService() = %q, want %q", got, tt.wantExact)
			}

			if len(got) > flyNameMaxLen {
				t.Errorf("flyAppNameForService() length = %d, exceeds max %d", len(got), flyNameMaxLen)
			}
		})
	}
}
