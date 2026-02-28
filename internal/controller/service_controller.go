// Package controller implements the Kubernetes controller for Service type: LoadBalancer.
package controller

import (
	"context"
	"fmt"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/zhming0/fly-tunnel-operator/internal/tunnel"
)

const (
	// DefaultLoadBalancerClass is the default loadBalancerClass to watch.
	DefaultLoadBalancerClass = "fly-tunnel-operator.dev/lb"

	// FinalizerName is the finalizer added to managed Services for cleanup.
	FinalizerName = "fly-tunnel-operator.dev/finalizer"
)

// ServiceReconciler reconciles Service objects with type LoadBalancer
// and the matching loadBalancerClass.
type ServiceReconciler struct {
	client            client.Client
	tunnelManager     *tunnel.Manager
	loadBalancerClass string
}

// NewServiceReconciler creates a new ServiceReconciler.
func NewServiceReconciler(
	client client.Client,
	tunnelManager *tunnel.Manager,
	loadBalancerClass string,
) *ServiceReconciler {
	if loadBalancerClass == "" {
		loadBalancerClass = DefaultLoadBalancerClass
	}
	return &ServiceReconciler{
		client:            client,
		tunnelManager:     tunnelManager,
		loadBalancerClass: loadBalancerClass,
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ServiceReconciler) SetupWithManager(mgr manager.Manager) error {
	return builder.ControllerManagedBy(mgr).
		For(&corev1.Service{}, builder.WithPredicates(r.serviceFilter())).
		Complete(r)
}

// Reconcile handles creating, updating, and deleting tunnel infrastructure
// for matching LoadBalancer services.
func (r *ServiceReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx).WithValues("service", req.NamespacedName)
	ctx = log.IntoContext(ctx, logger)

	// Fetch the Service.
	var svc corev1.Service
	if err := r.client.Get(ctx, req.NamespacedName, &svc); err != nil {
		if errors.IsNotFound(err) {
			// Service was deleted; nothing to do (finalizer handles cleanup).
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("getting service: %w", err)
	}

	// Check if this Service matches our loadBalancerClass.
	if !r.isManaged(&svc) {
		return reconcile.Result{}, nil
	}

	// Handle deletion via finalizer.
	if !svc.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &svc)
	}

	// Ensure finalizer is present.
	if !controllerutil.ContainsFinalizer(&svc, FinalizerName) {
		controllerutil.AddFinalizer(&svc, FinalizerName)
		if err := r.client.Update(ctx, &svc); err != nil {
			return reconcile.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		// Re-fetch after update.
		if err := r.client.Get(ctx, req.NamespacedName, &svc); err != nil {
			return reconcile.Result{}, fmt.Errorf("re-fetching service: %w", err)
		}
	}

	// Check if tunnel is already provisioned.
	if flyApp, ok := svc.Annotations[tunnel.AnnotationFlyApp]; ok && flyApp != "" {
		return r.reconcileUpdate(ctx, &svc)
	}

	// No tunnel yet — provision one.
	return r.reconcileCreate(ctx, &svc)
}

// reconcileCreate provisions a new tunnel for the Service.
func (r *ServiceReconciler) reconcileCreate(ctx context.Context, svc *corev1.Service) (reconcile.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Provisioning tunnel for Service")

	result, err := r.tunnelManager.Provision(ctx, svc)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("provisioning tunnel: %w", err)
	}

	// Re-fetch the Service to get the latest version before patching.
	key := client.ObjectKeyFromObject(svc)
	if err := r.client.Get(ctx, key, svc); err != nil {
		return reconcile.Result{}, fmt.Errorf("re-fetching service: %w", err)
	}

	// Store tunnel state in annotations.
	if svc.Annotations == nil {
		svc.Annotations = make(map[string]string)
	}
	svc.Annotations[tunnel.AnnotationFlyApp] = result.FlyApp
	svc.Annotations[tunnel.AnnotationMachineID] = result.MachineID
	svc.Annotations[tunnel.AnnotationFrpcDeployment] = result.FrpcDeployment
	svc.Annotations[tunnel.AnnotationIPID] = result.IPID
	svc.Annotations[tunnel.AnnotationPublicIP] = result.PublicIP

	if err := r.client.Update(ctx, svc); err != nil {
		return reconcile.Result{}, fmt.Errorf("updating service annotations: %w", err)
	}

	// Patch the Service status with the public IP.
	// Use MergeFrom patch to avoid conflicts with concurrent reconciliations.
	statusPatch := client.MergeFrom(svc.DeepCopy())
	svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{
		{IP: result.PublicIP},
	}
	if err := r.client.Status().Patch(ctx, svc, statusPatch); err != nil {
		return reconcile.Result{}, fmt.Errorf("updating service status: %w", err)
	}

	logger.Info("Tunnel provisioned successfully", "publicIP", result.PublicIP, "machineID", result.MachineID)
	return reconcile.Result{}, nil
}

// reconcileUpdate ensures an existing tunnel's configuration and status are up to date.
func (r *ServiceReconciler) reconcileUpdate(ctx context.Context, svc *corev1.Service) (reconcile.Result, error) {
	logger := log.FromContext(ctx)

	// Check if the Service status already has the correct IP.
	publicIP := svc.Annotations[tunnel.AnnotationPublicIP]
	needsStatusUpdate := len(svc.Status.LoadBalancer.Ingress) == 0 ||
		svc.Status.LoadBalancer.Ingress[0].IP != publicIP

	if needsStatusUpdate {
		statusPatch := client.MergeFrom(svc.DeepCopy())
		svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{
			{IP: publicIP},
		}
		if err := r.client.Status().Patch(ctx, svc, statusPatch); err != nil {
			return reconcile.Result{}, fmt.Errorf("updating service status: %w", err)
		}
		logger.Info("Updated Service status with public IP", "publicIP", publicIP)
	}

	// Detect if ports have changed and update the tunnel.
	// The tunnel manager will regenerate frpc config and update the Machine.
	if err := r.tunnelManager.Update(ctx, svc); err != nil {
		logger.Error(err, "Failed to update tunnel")
		// Don't return error — the tunnel may still be functional with old config.
		// The next reconciliation will retry.
	}

	return reconcile.Result{}, nil
}

// reconcileDelete tears down the tunnel and removes the finalizer.
func (r *ServiceReconciler) reconcileDelete(ctx context.Context, svc *corev1.Service) (reconcile.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Tearing down tunnel for deleted Service")

	if err := r.tunnelManager.Teardown(ctx, svc); err != nil {
		return reconcile.Result{}, fmt.Errorf("tearing down tunnel: %w", err)
	}

	// Remove the finalizer.
	controllerutil.RemoveFinalizer(svc, FinalizerName)
	if err := r.client.Update(ctx, svc); err != nil {
		return reconcile.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}

	logger.Info("Tunnel teardown complete")
	return reconcile.Result{}, nil
}

// isManaged returns true if the Service should be managed by this operator.
func (r *ServiceReconciler) isManaged(svc *corev1.Service) bool {
	if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
		return false
	}
	if svc.Spec.LoadBalancerClass == nil {
		return false
	}
	return *svc.Spec.LoadBalancerClass == r.loadBalancerClass
}

// serviceFilter returns a predicate that filters for matching LoadBalancer services.
func (r *ServiceReconciler) serviceFilter() predicate.Predicate {
	return predicate.Funcs{
		// Create: only if the Service is a LoadBalancer with matching loadBalancerClass.
		CreateFunc: func(e event.CreateEvent) bool {
			svc, ok := e.Object.(*corev1.Service)
			if !ok {
				return false
			}
			return r.isManaged(svc)
		},
		// Update: only if managed AND ports changed, annotations changed, deletion
		// started, or status is stale/missing.
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldSvc, ok1 := e.ObjectOld.(*corev1.Service)
			newSvc, ok2 := e.ObjectNew.(*corev1.Service)
			if !ok1 || !ok2 {
				return false
			}
			if !r.isManaged(newSvc) {
				return false
			}
			if !reflect.DeepEqual(oldSvc.Spec.Ports, newSvc.Spec.Ports) {
				return true
			}
			if !reflect.DeepEqual(oldSvc.Annotations, newSvc.Annotations) {
				return true
			}
			if !newSvc.DeletionTimestamp.IsZero() {
				return true
			}
			// Reconcile if status is missing or doesn't match the expected IP.
			if len(newSvc.Status.LoadBalancer.Ingress) == 0 {
				return true
			}
			expectedIP := newSvc.Annotations[tunnel.AnnotationPublicIP]
			if expectedIP != "" && newSvc.Status.LoadBalancer.Ingress[0].IP != expectedIP {
				return true
			}
			return false
		},
		// Delete: only if managed.
		DeleteFunc: func(e event.DeleteEvent) bool {
			svc, ok := e.Object.(*corev1.Service)
			if !ok {
				return false
			}
			return r.isManaged(svc)
		},
		// Generic: always ignored.
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	}
}
