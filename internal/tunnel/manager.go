// Package tunnel manages the lifecycle of fly.io Machine + frpc Deployment pairs.
package tunnel

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/zhming0/fly-tunnel-operator/internal/flyio"
	"github.com/zhming0/fly-tunnel-operator/internal/frp"
)

const (
	// Annotation keys used on the Service to track tunnel state.
	AnnotationMachineID      = "fly-tunnel-operator.dev/machine-id"
	AnnotationFrpcDeployment = "fly-tunnel-operator.dev/frpc-deployment"
	AnnotationIPID           = "fly-tunnel-operator.dev/ip-id"
	AnnotationPublicIP       = "fly-tunnel-operator.dev/public-ip"
	AnnotationFlyApp         = "fly-tunnel-operator.dev/fly-app"
	AnnotationTunnelGroup    = "fly-tunnel-operator.dev/tunnel-group"
	AnnotationFlyRegion      = "fly-tunnel-operator.dev/fly-region"
	AnnotationFlyMachineSize = "fly-tunnel-operator.dev/fly-machine-size"
)

// Config holds operator-level configuration.
type Config struct {
	FlyOrg            string
	FlyRegion         string
	FlyMachineSize    string
	FrpsImage         string
	FrpcImage         string
	OperatorNamespace string
}

// Manager handles creating and destroying tunnel infrastructure.
type Manager struct {
	flyClient  *flyio.Client
	kubeClient client.Client
	config     Config
}

// NewManager creates a new tunnel Manager.
func NewManager(flyClient *flyio.Client, kubeClient client.Client, config Config) *Manager {
	return &Manager{
		flyClient:  flyClient,
		kubeClient: kubeClient,
		config:     config,
	}
}

// TunnelResult contains the result of provisioning a tunnel.
type TunnelResult struct {
	FlyApp         string
	MachineID      string
	PublicIP       string
	IPID           string
	FrpcDeployment string
}

// Provision creates a dedicated fly.io App with a Machine running frps,
// deploys frpc in-cluster, and returns the public IP for the Service.
func (m *Manager) Provision(ctx context.Context, svc *corev1.Service) (*TunnelResult, error) {
	logger := log.FromContext(ctx)
	tunnelName := tunnelNameForService(svc)
	flyAppName := flyAppNameForService(svc)

	// Determine region (per-service override or default).
	region := m.config.FlyRegion
	if r, ok := svc.Annotations[AnnotationFlyRegion]; ok && r != "" {
		region = r
	}

	// Create a dedicated Fly App for this tunnel.
	logger.Info("Creating fly.io App", "app", flyAppName, "org", m.config.FlyOrg)
	if err := m.flyClient.CreateApp(ctx, flyAppName, m.config.FlyOrg); err != nil {
		return nil, fmt.Errorf("creating fly app: %w", err)
	}

	// Build fly.io Machine services configuration.
	// Port 7000 for frp control channel + all service ports.
	machineServices := []flyio.MachineService{
		{
			Protocol:     "tcp",
			InternalPort: frp.DefaultServerPort,
			Ports: []flyio.Port{
				{Port: frp.DefaultServerPort},
			},
		},
	}
	for _, port := range svc.Spec.Ports {
		machineServices = append(machineServices, flyio.MachineService{
			Protocol:     "tcp",
			InternalPort: int(port.Port),
			Ports: []flyio.Port{
				{Port: int(port.Port)},
			},
		})
	}

	// Determine guest config based on machine size.
	guest := guestForSize(m.config.FlyMachineSize)
	if size, ok := svc.Annotations[AnnotationFlyMachineSize]; ok && size != "" {
		guest = guestForSize(size)
	}

	// Generate frps config and inject it via init command.
	frpsConfig := frp.GenerateServerConfig(frp.DefaultServerPort)

	// Create the fly.io Machine running frps.
	logger.Info("Creating fly.io Machine", "name", tunnelName, "app", flyAppName, "region", region)
	machine, err := m.flyClient.CreateMachine(ctx, flyAppName, flyio.CreateMachineInput{
		Name:   tunnelName,
		Region: region,
		Config: flyio.MachineConfig{
			Image:    m.config.FrpsImage,
			Guest:    guest,
			Services: machineServices,
			Env: map[string]string{
				"FRP_SERVER_CONFIG": frpsConfig,
			},
			Init: &flyio.InitConfig{
				Entrypoint: []string{"sh"},
				Cmd: []string{"-c",
					"mkdir -p /etc/frp && echo \"$FRP_SERVER_CONFIG\" > /etc/frp/frps.toml && exec frps -c /etc/frp/frps.toml",
				},
			},
		},
	})
	if err != nil {
		_ = m.flyClient.DeleteApp(ctx, flyAppName)
		return nil, fmt.Errorf("creating fly machine: %w", err)
	}
	logger.Info("Machine created", "machineID", machine.ID, "instanceID", machine.InstanceID)

	// Wait for the Machine to start.
	if err := m.flyClient.WaitForMachine(ctx, flyAppName, machine.ID, machine.InstanceID, "started", 60*time.Second); err != nil {
		_ = m.flyClient.DeleteMachine(ctx, flyAppName, machine.ID)
		_ = m.flyClient.DeleteApp(ctx, flyAppName)
		return nil, fmt.Errorf("waiting for machine to start: %w", err)
	}

	// Allocate a dedicated IPv4.
	logger.Info("Allocating dedicated IPv4", "app", flyAppName)
	ip, err := m.flyClient.AllocateDedicatedIPv4(ctx, flyAppName)
	if err != nil {
		_ = m.flyClient.DeleteMachine(ctx, flyAppName, machine.ID)
		_ = m.flyClient.DeleteApp(ctx, flyAppName)
		return nil, fmt.Errorf("allocating dedicated IPv4: %w", err)
	}
	logger.Info("IPv4 allocated", "address", ip.Address, "id", ip.ID)

	// Deploy frpc in-cluster.
	frpcDeploymentName := frpcDeploymentNameForService(svc)
	if err := m.deployFrpc(ctx, svc, ip.Address, frpcDeploymentName); err != nil {
		_ = m.flyClient.ReleaseIPAddress(ctx, flyAppName, ip.ID)
		_ = m.flyClient.DeleteMachine(ctx, flyAppName, machine.ID)
		_ = m.flyClient.DeleteApp(ctx, flyAppName)
		return nil, fmt.Errorf("deploying frpc: %w", err)
	}

	return &TunnelResult{
		FlyApp:         flyAppName,
		MachineID:      machine.ID,
		PublicIP:       ip.Address,
		IPID:           ip.ID,
		FrpcDeployment: frpcDeploymentName,
	}, nil
}

// Teardown destroys the tunnel infrastructure for a Service.
func (m *Manager) Teardown(ctx context.Context, svc *corev1.Service) error {
	logger := log.FromContext(ctx)

	flyAppName := svc.Annotations[AnnotationFlyApp]

	// Delete frpc Deployment and ConfigMap.
	if deployName, ok := svc.Annotations[AnnotationFrpcDeployment]; ok && deployName != "" {
		logger.Info("Deleting frpc Deployment", "name", deployName)
		if err := m.deleteFrpcResources(ctx, deployName); err != nil {
			logger.Error(err, "Failed to delete frpc resources", "name", deployName)
		}
	}

	if flyAppName != "" {
		// Release the dedicated IPv4.
		if ipID, ok := svc.Annotations[AnnotationIPID]; ok && ipID != "" {
			logger.Info("Releasing dedicated IPv4", "id", ipID)
			if err := m.flyClient.ReleaseIPAddress(ctx, flyAppName, ipID); err != nil {
				logger.Error(err, "Failed to release IP", "id", ipID)
			}
		}

		// Delete the fly.io Machine.
		if machineID, ok := svc.Annotations[AnnotationMachineID]; ok && machineID != "" {
			logger.Info("Deleting fly.io Machine", "id", machineID)
			if err := m.flyClient.DeleteMachine(ctx, flyAppName, machineID); err != nil {
				logger.Error(err, "Failed to delete machine", "id", machineID)
			}
		}

		// Delete the Fly App.
		logger.Info("Deleting fly.io App", "app", flyAppName)
		if err := m.flyClient.DeleteApp(ctx, flyAppName); err != nil {
			logger.Error(err, "Failed to delete fly app", "app", flyAppName)
		}
	}

	return nil
}

// Update regenerates frpc config and restarts the frpc Deployment when ports change.
func (m *Manager) Update(ctx context.Context, svc *corev1.Service) error {
	logger := log.FromContext(ctx)
	publicIP := svc.Annotations[AnnotationPublicIP]
	deployName := svc.Annotations[AnnotationFrpcDeployment]
	machineID := svc.Annotations[AnnotationMachineID]
	flyAppName := svc.Annotations[AnnotationFlyApp]

	if publicIP == "" || deployName == "" || flyAppName == "" {
		return fmt.Errorf("service missing tunnel annotations, cannot update")
	}

	// Regenerate frpc ConfigMap.
	configMapName := deployName + "-config"
	configData := frp.GenerateClientConfig(svc, publicIP, frp.DefaultServerPort)

	var existingCM corev1.ConfigMap
	if err := m.kubeClient.Get(ctx, types.NamespacedName{
		Name:      configMapName,
		Namespace: m.config.OperatorNamespace,
	}, &existingCM); err != nil {
		return fmt.Errorf("getting frpc configmap: %w", err)
	}

	existingCM.Data["frpc.toml"] = configData
	if err := m.kubeClient.Update(ctx, &existingCM); err != nil {
		return fmt.Errorf("updating frpc configmap: %w", err)
	}
	logger.Info("Updated frpc ConfigMap", "name", configMapName)

	// Restart the Deployment by updating an annotation to trigger a rollout.
	var deploy appsv1.Deployment
	if err := m.kubeClient.Get(ctx, types.NamespacedName{
		Name:      deployName,
		Namespace: m.config.OperatorNamespace,
	}, &deploy); err != nil {
		return fmt.Errorf("getting frpc deployment: %w", err)
	}

	if deploy.Spec.Template.Annotations == nil {
		deploy.Spec.Template.Annotations = make(map[string]string)
	}
	deploy.Spec.Template.Annotations["fly-tunnel-operator.dev/restart-at"] = time.Now().Format(time.RFC3339)
	if err := m.kubeClient.Update(ctx, &deploy); err != nil {
		return fmt.Errorf("updating frpc deployment: %w", err)
	}
	logger.Info("Restarted frpc Deployment", "name", deployName)

	// Update fly.io Machine services for new ports.
	if machineID != "" {
		machineServices := []flyio.MachineService{
			{
				Protocol:     "tcp",
				InternalPort: frp.DefaultServerPort,
				Ports: []flyio.Port{
					{Port: frp.DefaultServerPort},
				},
			},
		}
		for _, port := range svc.Spec.Ports {
			machineServices = append(machineServices, flyio.MachineService{
				Protocol:     "tcp",
				InternalPort: int(port.Port),
				Ports: []flyio.Port{
					{Port: int(port.Port)},
				},
			})
		}

		tunnelName := tunnelNameForService(svc)
		region := m.config.FlyRegion
		if r, ok := svc.Annotations[AnnotationFlyRegion]; ok && r != "" {
			region = r
		}

		frpsConfig := frp.GenerateServerConfig(frp.DefaultServerPort)
		_, err := m.flyClient.UpdateMachine(ctx, flyAppName, machineID, flyio.CreateMachineInput{
			Name:   tunnelName,
			Region: region,
			Config: flyio.MachineConfig{
				Image:    m.config.FrpsImage,
				Services: machineServices,
				Env: map[string]string{
					"FRP_SERVER_CONFIG": frpsConfig,
				},
				Init: &flyio.InitConfig{
					Entrypoint: []string{"sh"},
					Cmd: []string{"-c",
						"mkdir -p /etc/frp && echo \"$FRP_SERVER_CONFIG\" > /etc/frp/frps.toml && exec frps -c /etc/frp/frps.toml",
					},
				},
			},
		})
		if err != nil {
			return fmt.Errorf("updating fly machine: %w", err)
		}
		logger.Info("Updated fly.io Machine services", "machineID", machineID)
	}

	return nil
}

// deployFrpc creates the frpc ConfigMap and Deployment in-cluster.
func (m *Manager) deployFrpc(ctx context.Context, svc *corev1.Service, serverAddr, deploymentName string) error {
	configMapName := deploymentName + "-config"
	configData := frp.GenerateClientConfig(svc, serverAddr, frp.DefaultServerPort)

	// Create ConfigMap with frpc config.
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: m.config.OperatorNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":          "frpc",
				"app.kubernetes.io/managed-by":    "fly-tunnel-operator",
				"fly-tunnel-operator.dev/service": serviceLabelValue(svc),
			},
		},
		Data: map[string]string{
			"frpc.toml": configData,
		},
	}

	if err := m.kubeClient.Create(ctx, cm); err != nil {
		if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("creating frpc configmap: %w", err)
		}
		// Update existing ConfigMap.
		var existing corev1.ConfigMap
		if err := m.kubeClient.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: m.config.OperatorNamespace}, &existing); err != nil {
			return fmt.Errorf("getting existing frpc configmap: %w", err)
		}
		existing.Data = cm.Data
		if err := m.kubeClient.Update(ctx, &existing); err != nil {
			return fmt.Errorf("updating existing frpc configmap: %w", err)
		}
	}

	// Create frpc Deployment.
	labels := map[string]string{
		"app.kubernetes.io/name":       "frpc",
		"app.kubernetes.io/instance":   deploymentName,
		"app.kubernetes.io/managed-by": "fly-tunnel-operator",
	}

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: m.config.OperatorNamespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    "frpc",
							Image:   m.config.FrpcImage,
							Command: []string{"frpc"},
							Args:    []string{"-c", "/etc/frp/frpc.toml"},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "config",
									MountPath: "/etc/frp",
									ReadOnly:  true,
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: configMapName,
									},
								},
							},
						},
					},
				},
			},
		},
	}

	if err := m.kubeClient.Create(ctx, deploy); err != nil {
		if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("creating frpc deployment: %w", err)
		}
		// Update existing Deployment.
		var existing appsv1.Deployment
		if err := m.kubeClient.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: m.config.OperatorNamespace}, &existing); err != nil {
			return fmt.Errorf("getting existing frpc deployment: %w", err)
		}
		existing.Spec = deploy.Spec
		if err := m.kubeClient.Update(ctx, &existing); err != nil {
			return fmt.Errorf("updating existing frpc deployment: %w", err)
		}
	}

	return nil
}

// deleteFrpcResources removes the frpc Deployment and ConfigMap.
func (m *Manager) deleteFrpcResources(ctx context.Context, deploymentName string) error {
	configMapName := deploymentName + "-config"

	// Delete Deployment.
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: m.config.OperatorNamespace,
		},
	}
	if err := m.kubeClient.Delete(ctx, deploy); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("deleting frpc deployment: %w", err)
	}

	// Delete ConfigMap.
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: m.config.OperatorNamespace,
		},
	}
	if err := m.kubeClient.Delete(ctx, cm); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("deleting frpc configmap: %w", err)
	}

	return nil
}

func guestForSize(size string) *flyio.GuestConfig {
	switch size {
	case "shared-cpu-2x":
		return &flyio.GuestConfig{CPUKind: "shared", CPUs: 2, MemoryMB: 512}
	case "shared-cpu-4x":
		return &flyio.GuestConfig{CPUKind: "shared", CPUs: 4, MemoryMB: 1024}
	case "performance-1x":
		return &flyio.GuestConfig{CPUKind: "performance", CPUs: 1, MemoryMB: 2048}
	case "performance-2x":
		return &flyio.GuestConfig{CPUKind: "performance", CPUs: 2, MemoryMB: 4096}
	default: // shared-cpu-1x
		return &flyio.GuestConfig{CPUKind: "shared", CPUs: 1, MemoryMB: 256}
	}
}
