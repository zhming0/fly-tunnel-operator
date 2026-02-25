package tunnel

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// maxLabelLen is the maximum length for both Fly.io app names and
// Kubernetes label values (both 63 characters).
const maxLabelLen = 63

func tunnelNameForService(svc *corev1.Service) string {
	return sanitizeName(fmt.Sprintf("frp-%s-%s", svc.Namespace, svc.Name))
}

func flyAppNameForService(svc *corev1.Service) string {
	return sanitizeName(fmt.Sprintf("fly-tunnel-%s-%s", svc.Namespace, svc.Name))
}

func frpcDeploymentNameForService(svc *corev1.Service) string {
	return sanitizeName(fmt.Sprintf("frpc-%s-%s", svc.Namespace, svc.Name))
}

func serviceLabelValue(svc *corev1.Service) string {
	return sanitizeName(fmt.Sprintf("%s-%s", svc.Namespace, svc.Name))
}

// sanitizeName produces a string safe for both Fly.io app names and
// Kubernetes label values: lowercase alphanumerics and dashes, at most
// 63 characters. When truncation is needed a short hash suffix preserves
// uniqueness.
func sanitizeName(name string) string {
	name = strings.ToLower(name)

	var b strings.Builder
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			b.WriteRune(c)
		} else {
			b.WriteRune('-')
		}
	}
	sanitized := b.String()

	// Collapse consecutive dashes and trim leading/trailing dashes.
	for strings.Contains(sanitized, "--") {
		sanitized = strings.ReplaceAll(sanitized, "--", "-")
	}
	sanitized = strings.Trim(sanitized, "-")

	if len(sanitized) <= maxLabelLen {
		return sanitized
	}

	// Truncate with a hash suffix for uniqueness.
	hash := sha256.Sum256([]byte(name))
	suffix := hex.EncodeToString(hash[:4]) // 8 hex chars
	// Leave room for dash + 8-char suffix.
	truncated := sanitized[:maxLabelLen-len(suffix)-1]
	truncated = strings.TrimRight(truncated, "-")
	return truncated + "-" + suffix
}
