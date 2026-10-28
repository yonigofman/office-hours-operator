/*
Copyright 2026 Yoni Gofman.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	schedulingv1alpha1 "github.com/yonigofman/office-hours-operator/api/v1alpha1"
	"github.com/yonigofman/office-hours-operator/internal/schedule"
)

// OfficeHoursReconciler reconciles a OfficeHours object
type OfficeHoursReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=scheduling.yonigofman.github.io,resources=officehours,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=scheduling.yonigofman.github.io,resources=officehours/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=scheduling.yonigofman.github.io,resources=officehours/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the OfficeHours object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.3/pkg/reconcile
func (r *OfficeHoursReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = logf.FromContext(ctx)

	var oh schedulingv1alpha1.OfficeHours
	if err := r.Get(ctx, req.NamespacedName, &oh); err != nil {
		// Deleted since the event was queued? Nothing to do. Any other error: retry.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	days := make([]string, 0, len(oh.Spec.Workdays))
	for _, d := range oh.Spec.Workdays {
		days = append(days, string(d))
	}

	sched, err := schedule.Parse(oh.Spec.TimeZone, days, oh.Spec.Start, oh.Spec.End)

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *OfficeHoursReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&schedulingv1alpha1.OfficeHours{}).
		Named("officehours").
		Complete(r)
}
