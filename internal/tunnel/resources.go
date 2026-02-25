package tunnel

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	// Per-service annotations for overriding frpc pod resources.
	AnnotationFrpcCPURequest    = "fly-tunnel-operator.dev/frpc-cpu-request"
	AnnotationFrpcCPULimit      = "fly-tunnel-operator.dev/frpc-cpu-limit"
	AnnotationFrpcMemoryRequest = "fly-tunnel-operator.dev/frpc-memory-request"
	AnnotationFrpcMemoryLimit   = "fly-tunnel-operator.dev/frpc-memory-limit"
)

var defaultFrpcResources = corev1.ResourceRequirements{
	Requests: corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("10m"),
		corev1.ResourceMemory: resource.MustParse("32Mi"),
	},
	Limits: corev1.ResourceList{
		corev1.ResourceMemory: resource.MustParse("128Mi"),
	},
}

// resourceAnnotationOverrides maps each resource annotation to where it should
// be applied in the ResourceRequirements.
var resourceAnnotationOverrides = []struct {
	annotation   string
	resourceName corev1.ResourceName
	target       func(*corev1.ResourceRequirements) corev1.ResourceList
}{
	{AnnotationFrpcCPURequest, corev1.ResourceCPU, func(r *corev1.ResourceRequirements) corev1.ResourceList { return r.Requests }},
	{AnnotationFrpcCPULimit, corev1.ResourceCPU, func(r *corev1.ResourceRequirements) corev1.ResourceList { return r.Limits }},
	{AnnotationFrpcMemoryRequest, corev1.ResourceMemory, func(r *corev1.ResourceRequirements) corev1.ResourceList { return r.Requests }},
	{AnnotationFrpcMemoryLimit, corev1.ResourceMemory, func(r *corev1.ResourceRequirements) corev1.ResourceList { return r.Limits }},
}

// frpcResources returns the resource requirements for the frpc container,
// using per-service annotation overrides when present.
func frpcResources(svc *corev1.Service) (corev1.ResourceRequirements, error) {
	res := *defaultFrpcResources.DeepCopy()

	for _, o := range resourceAnnotationOverrides {
		v, ok := svc.Annotations[o.annotation]
		if !ok || v == "" {
			continue
		}
		q, err := resource.ParseQuantity(v)
		if err != nil {
			return res, fmt.Errorf("parsing annotation %s=%q: %w", o.annotation, v, err)
		}
		o.target(&res)[o.resourceName] = q
	}

	return res, nil
}
