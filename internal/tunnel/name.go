package tunnel

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

const flyNameMaxLen = 63

func tunnelNameForService(svc *corev1.Service) string {
	return sanitizeFlyName(fmt.Sprintf("frp-%s-%s", svc.Namespace, svc.Name))
}

func flyAppNameForService(svc *corev1.Service) string {
	return sanitizeFlyName(fmt.Sprintf("fly-tunnel-%s-%s", svc.Namespace, svc.Name))
}

// sanitizeFlyName enforces Fly.io app naming rules:
// under 63 chars, lowercase letters, numbers, and dashes only.
// When truncation is needed, a short hash suffix preserves uniqueness.
func sanitizeFlyName(name string) string {
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

	if len(sanitized) <= flyNameMaxLen {
		return sanitized
	}

	// Truncate with a hash suffix for uniqueness.
	hash := sha256.Sum256([]byte(name))
	suffix := hex.EncodeToString(hash[:4]) // 8 hex chars
	// Leave room for dash + 8-char suffix.
	truncated := sanitized[:flyNameMaxLen-len(suffix)-1]
	truncated = strings.TrimRight(truncated, "-")
	return truncated + "-" + suffix
}
