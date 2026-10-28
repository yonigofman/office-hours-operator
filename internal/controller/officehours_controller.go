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
	"errors"
	"fmt"
	"maps"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	schedulingv1alpha1 "github.com/yonigofman/office-hours-operator/api/v1alpha1"
	"github.com/yonigofman/office-hours-operator/internal/schedule"
)

const (
	// annOriginalReplicas remembers how many replicas to restore in the morning.
	annOriginalReplicas = "scheduling.yonigofman.github.io/original-replicas"
	// annManagedBy names the OfficeHours that put a workload to sleep, so two
	// schedules never fight over the same workload.
	annManagedBy = "scheduling.yonigofman.github.io/managed-by"
	// finalizerName lets the controller wake every workload it put to sleep
	// before its OfficeHours is deleted.
	finalizerName = "scheduling.yonigofman.github.io/wake-on-delete"

	reasonScheduleApplied     = "ScheduleApplied"
	reasonInvalidTimeZone     = "InvalidTimeZone"
	reasonInvalidSchedule     = "InvalidSchedule"
	reasonOverlappingSchedule = "OverlappingSchedules"
	reasonApplyFailed         = "ApplyFailed"
)

// OfficeHoursReconciler reconciles a OfficeHours object
type OfficeHoursReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Clock is injected so tests can control time. Use clock.RealClock{} in main.
	Clock clock.PassiveClock
}

// +kubebuilder:rbac:groups=scheduling.yonigofman.github.io,resources=officehours,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=scheduling.yonigofman.github.io,resources=officehours/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=scheduling.yonigofman.github.io,resources=officehours/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

// Reconcile puts the selected workloads to sleep outside office hours, wakes
// them up inside office hours, and then waits until the next transition.
func (r *OfficeHoursReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var oh schedulingv1alpha1.OfficeHours
	if err := r.Get(ctx, req.NamespacedName, &oh); err != nil {
		// Deleted since the event was queued? Nothing to do. Any other error: retry.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Being deleted: wake everything this schedule put to sleep, then let go.
	if !oh.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.finalize(ctx, &oh)
	}
	if controllerutil.AddFinalizer(&oh, finalizerName) {
		if err := r.Update(ctx, &oh); err != nil {
			return ctrl.Result{}, err
		}
	}

	sched, err := parseSchedule(oh.Spec)
	if err != nil {
		// Fail open: an invalid schedule must never leave environments switched off.
		if _, wakeErr := r.apply(ctx, &oh, nil, true); wakeErr != nil {
			return ctrl.Result{}, wakeErr
		}
		reason := reasonInvalidSchedule
		if errors.Is(err, schedule.ErrUnknownTimeZone) {
			reason = reasonInvalidTimeZone
		}
		// Retrying cannot fix a bad spec; the next spec change triggers a new reconcile.
		return ctrl.Result{}, r.updateStatus(ctx, &oh, func(s *schedulingv1alpha1.OfficeHoursStatus) {
			s.Phase, s.NextTransition, s.ManagedWorkloads = "", nil, 0
			setReady(&oh, metav1.ConditionFalse, reason, err.Error())
		})
	}

	selector, err := metav1.LabelSelectorAsSelector(&oh.Spec.NamespaceSelector)
	if err != nil {
		return ctrl.Result{}, r.updateStatus(ctx, &oh, func(*schedulingv1alpha1.OfficeHoursStatus) {
			setReady(&oh, metav1.ConditionFalse, reasonInvalidSchedule, err.Error())
		})
	}

	now := r.Clock.Now()
	awake, next := sched.Evaluate(now)
	phase := schedulingv1alpha1.PhaseAsleep
	if awake {
		phase = schedulingv1alpha1.PhaseAwake
	}

	result, err := r.apply(ctx, &oh, selector, awake)
	if err != nil {
		_ = r.updateStatus(ctx, &oh, func(*schedulingv1alpha1.OfficeHoursStatus) {
			setReady(&oh, metav1.ConditionFalse, reasonApplyFailed, err.Error())
		})
		return ctrl.Result{}, err
	}

	err = r.updateStatus(ctx, &oh, func(s *schedulingv1alpha1.OfficeHoursStatus) {
		s.Phase = phase
		s.NextTransition = &metav1.Time{Time: next}
		s.ManagedWorkloads = result.managed
		if result.conflicts > 0 {
			setReady(&oh, metav1.ConditionFalse, reasonOverlappingSchedule, fmt.Sprintf(
				"%d workloads are already managed by another OfficeHours and were skipped", result.conflicts))
			return
		}
		setReady(&oh, metav1.ConditionTrue, reasonScheduleApplied, fmt.Sprintf(
			"%s until %s", phase, next.Format("Mon 2006-01-02 15:04 MST")))
	})

	log.Info("schedule applied", "phase", phase, "next", next, "managed", result.managed,
		"conflicts", result.conflicts)

	// Sleep until the next transition. The extra second makes sure we wake up
	// after the boundary, not a few milliseconds before it.
	return ctrl.Result{RequeueAfter: next.Sub(now) + time.Second}, err
}

// parseSchedule converts the API types into the schedule package's input.
func parseSchedule(spec schedulingv1alpha1.OfficeHoursSpec) (schedule.Schedule, error) {
	days := make([]string, 0, len(spec.Workdays))
	for _, d := range spec.Workdays {
		days = append(days, string(d))
	}
	return schedule.Parse(spec.TimeZone, days, spec.Start, spec.End)
}

// finalize wakes every workload this schedule put to sleep and removes the finalizer.
func (r *OfficeHoursReconciler) finalize(ctx context.Context, oh *schedulingv1alpha1.OfficeHours) error {
	if !controllerutil.ContainsFinalizer(oh, finalizerName) {
		return nil
	}
	if _, err := r.apply(ctx, oh, nil, true); err != nil {
		return err
	}
	controllerutil.RemoveFinalizer(oh, finalizerName)
	return r.Update(ctx, oh)
}

// workload is a Deployment or StatefulSet, seen through the one field we change.
type workload struct {
	obj      client.Object
	replicas **int32 // points at the object's .spec.replicas
}

type applyResult struct {
	managed   int32
	conflicts int32
}

// apply makes every Deployment and StatefulSet match the schedule. A nil
// selector selects nothing, which wakes everything this schedule owns: that is
// how deletion and invalid schedules are handled.
func (r *OfficeHoursReconciler) apply(ctx context.Context, oh *schedulingv1alpha1.OfficeHours,
	selector labels.Selector, awake bool) (applyResult, error) {
	selected := map[string]bool{}
	if selector != nil {
		var namespaces corev1.NamespaceList
		if err := r.List(ctx, &namespaces, client.MatchingLabelsSelector{Selector: selector}); err != nil {
			return applyResult{}, err
		}
		for _, ns := range namespaces.Items {
			selected[ns.Name] = true
		}
	}

	workloads, err := r.listWorkloads(ctx)
	if err != nil {
		return applyResult{}, err
	}

	var result applyResult
	for _, w := range workloads {
		ann := w.obj.GetAnnotations()
		owner := ann[annManagedBy]
		ours := owner == oh.Name
		inScope := selected[w.obj.GetNamespace()] && ann[schedulingv1alpha1.ExcludeAnnotation] != "true"

		switch {
		case !inScope:
			// Namespace no longer selected, workload opted out, or schedule going away.
			if ours {
				err = r.wake(ctx, w)
			}
		case owner != "" && !ours:
			result.conflicts++
		default:
			result.managed++
			if !awake {
				err = r.sleep(ctx, w, oh.Name)
			} else if ours {
				err = r.wake(ctx, w)
			}
		}
		if err != nil {
			return result, fmt.Errorf("%s/%s: %w", w.obj.GetNamespace(), w.obj.GetName(), err)
		}
	}
	return result, nil
}

func (r *OfficeHoursReconciler) listWorkloads(ctx context.Context) ([]workload, error) {
	var deployments appsv1.DeploymentList
	if err := r.List(ctx, &deployments); err != nil {
		return nil, err
	}
	var statefulSets appsv1.StatefulSetList
	if err := r.List(ctx, &statefulSets); err != nil {
		return nil, err
	}
	workloads := make([]workload, 0, len(deployments.Items)+len(statefulSets.Items))
	for i := range deployments.Items {
		d := &deployments.Items[i]
		workloads = append(workloads, workload{obj: d, replicas: &d.Spec.Replicas})
	}
	for i := range statefulSets.Items {
		s := &statefulSets.Items[i]
		workloads = append(workloads, workload{obj: s, replicas: &s.Spec.Replicas})
	}
	return workloads, nil
}

// sleep scales a workload to zero, remembering its replica count the first time.
// If someone scales it up during the night, the schedule wins and it goes back
// to zero; to keep a workload running, use the exclude annotation.
func (r *OfficeHoursReconciler) sleep(ctx context.Context, w workload, owner string) error {
	current := int32(1) // Kubernetes defaults a missing replicas field to 1
	if *w.replicas != nil {
		current = **w.replicas
	}
	if current == 0 {
		return nil // already asleep, or scaled to zero by its owner: nothing to do
	}

	base := w.obj.DeepCopyObject().(client.Object)
	ann := maps.Clone(w.obj.GetAnnotations())
	if ann == nil {
		ann = map[string]string{}
	}
	if _, saved := ann[annOriginalReplicas]; !saved {
		ann[annOriginalReplicas] = strconv.Itoa(int(current))
		ann[annManagedBy] = owner
	}
	w.obj.SetAnnotations(ann)
	zero := int32(0)
	*w.replicas = &zero
	return r.Patch(ctx, w.obj, client.MergeFrom(base))
}

// wake restores the remembered replica count and removes our annotations.
func (r *OfficeHoursReconciler) wake(ctx context.Context, w workload) error {
	base := w.obj.DeepCopyObject().(client.Object)
	ann := maps.Clone(w.obj.GetAnnotations())
	if v, err := strconv.ParseInt(ann[annOriginalReplicas], 10, 32); err == nil {
		replicas := int32(v)
		*w.replicas = &replicas
	} // missing or corrupted: leave the replica count as it is
	delete(ann, annOriginalReplicas)
	delete(ann, annManagedBy)
	w.obj.SetAnnotations(ann)
	return r.Patch(ctx, w.obj, client.MergeFrom(base))
}

// updateStatus applies mutate to the status and sends only the difference.
func (r *OfficeHoursReconciler) updateStatus(ctx context.Context, oh *schedulingv1alpha1.OfficeHours,
	mutate func(*schedulingv1alpha1.OfficeHoursStatus)) error {
	base := oh.DeepCopy()
	mutate(&oh.Status)
	oh.Status.ObservedGeneration = oh.Generation
	return r.Status().Patch(ctx, oh, client.MergeFrom(base))
}

func setReady(oh *schedulingv1alpha1.OfficeHours, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&oh.Status.Conditions, metav1.Condition{
		Type:               schedulingv1alpha1.ConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: oh.Generation,
	})
}

// enqueueAll maps any change to a workload or namespace to every OfficeHours.
// There are only ever a handful of schedules, and Reconcile works out the rest.
func (r *OfficeHoursReconciler) enqueueAll(ctx context.Context, _ client.Object) []reconcile.Request {
	var list schedulingv1alpha1.OfficeHoursList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(list.Items))
	for _, oh := range list.Items {
		requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: oh.Name}})
	}
	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *OfficeHoursReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Workloads: react to spec and annotation changes, not to every status update.
	workloadChanged := builder.WithPredicates(predicate.Or[client.Object](
		predicate.GenerationChangedPredicate{}, predicate.AnnotationChangedPredicate{}))

	return ctrl.NewControllerManagedBy(mgr).
		For(&schedulingv1alpha1.OfficeHours{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&appsv1.Deployment{}, handler.EnqueueRequestsFromMapFunc(r.enqueueAll), workloadChanged).
		Watches(&appsv1.StatefulSet{}, handler.EnqueueRequestsFromMapFunc(r.enqueueAll), workloadChanged).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.enqueueAll),
			builder.WithPredicates(predicate.LabelChangedPredicate{})).
		Named("officehours").
		Complete(r)
}
