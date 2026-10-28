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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	testingclock "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	schedulingv1alpha1 "github.com/yonigofman/office-hours-operator/api/v1alpha1"
)

const optInLabel = "scheduling.yonigofman.github.io/office-hours"

func newDeployment(namespace, name string, replicas int32, annotations map[string]string) *appsv1.Deployment {
	labels := map[string]string{"app": name}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Annotations: annotations},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx"}}},
			},
		},
	}
}

func newOfficeHours(name, timeZone string) *schedulingv1alpha1.OfficeHours {
	return &schedulingv1alpha1.OfficeHours{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: schedulingv1alpha1.OfficeHoursSpec{
			NamespaceSelector: metav1.LabelSelector{MatchLabels: map[string]string{optInLabel: "enabled"}},
			TimeZone:          timeZone,
			Workdays: []schedulingv1alpha1.Weekday{
				schedulingv1alpha1.Sunday, schedulingv1alpha1.Monday, schedulingv1alpha1.Tuesday,
				schedulingv1alpha1.Wednesday, schedulingv1alpha1.Thursday,
			},
			Start: "08:00",
			End:   "19:00",
		},
	}
}

var _ = Describe("OfficeHours controller", Ordered, func() {
	const (
		selectedNS   = "team-a-dev"
		unselectedNS = "team-b-prod"
		scheduleName = "dev-hours"
	)

	var (
		jlm        *time.Location
		fakeClock  *testingclock.FakeClock
		reconciler *OfficeHoursReconciler
	)

	local := func(month time.Month, day, hour, minute int) time.Time {
		return time.Date(2026, month, day, hour, minute, 0, 0, jlm)
	}
	reconcileSchedule := func(name string) ctrl.Result {
		GinkgoHelper()
		result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: name}})
		Expect(err).NotTo(HaveOccurred())
		return result
	}
	getDeployment := func(namespace, name string) *appsv1.Deployment {
		GinkgoHelper()
		d := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, d)).To(Succeed())
		return d
	}
	getSchedule := func(name string) *schedulingv1alpha1.OfficeHours {
		GinkgoHelper()
		oh := &schedulingv1alpha1.OfficeHours{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, oh)).To(Succeed())
		return oh
	}

	BeforeAll(func() {
		var err error
		jlm, err = time.LoadLocation("Asia/Jerusalem")
		Expect(err).NotTo(HaveOccurred())

		// Sunday 2026-11-01, 12:00 in Jerusalem: inside office hours.
		fakeClock = testingclock.NewFakeClock(local(time.November, 1, 12, 0))
		reconciler = &OfficeHoursReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Clock: fakeClock}

		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: selectedNS, Labels: map[string]string{optInLabel: "enabled"}}})).To(Succeed())
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: unselectedNS}})).To(Succeed())

		Expect(k8sClient.Create(ctx, newDeployment(selectedNS, "web", 2, nil))).To(Succeed())
		Expect(k8sClient.Create(ctx, newDeployment(selectedNS, "mock", 1,
			map[string]string{schedulingv1alpha1.ExcludeAnnotation: "true"}))).To(Succeed())
		Expect(k8sClient.Create(ctx, newDeployment(unselectedNS, "api", 3, nil))).To(Succeed())

		Expect(k8sClient.Create(ctx, newOfficeHours(scheduleName, "Asia/Jerusalem"))).To(Succeed())
	})

	It("leaves everything running during office hours", func() {
		result := reconcileSchedule(scheduleName)

		Expect(*getDeployment(selectedNS, "web").Spec.Replicas).To(BeEquivalentTo(2))

		oh := getSchedule(scheduleName)
		Expect(oh.Finalizers).To(ContainElement(finalizerName))
		Expect(oh.Status.Phase).To(Equal(schedulingv1alpha1.PhaseAwake))
		Expect(oh.Status.NextTransition.Time).To(BeTemporally("==", local(time.November, 1, 19, 0)))
		Expect(oh.Status.ManagedWorkloads).To(BeEquivalentTo(1)) // web; mock is excluded
		Expect(meta.IsStatusConditionTrue(oh.Status.Conditions, schedulingv1alpha1.ConditionReady)).To(BeTrue())

		By("sleeping until the evening transition")
		Expect(result.RequeueAfter).To(Equal(7*time.Hour + time.Second))
	})

	It("scales selected workloads to zero after hours", func() {
		fakeClock.SetTime(local(time.November, 1, 20, 0))
		reconcileSchedule(scheduleName)

		web := getDeployment(selectedNS, "web")
		Expect(*web.Spec.Replicas).To(BeEquivalentTo(0))
		Expect(web.Annotations).To(HaveKeyWithValue(annOriginalReplicas, "2"))
		Expect(web.Annotations).To(HaveKeyWithValue(annManagedBy, scheduleName))

		Expect(*getDeployment(selectedNS, "mock").Spec.Replicas).To(BeEquivalentTo(1))  // opted out
		Expect(*getDeployment(unselectedNS, "api").Spec.Replicas).To(BeEquivalentTo(3)) // not selected

		oh := getSchedule(scheduleName)
		Expect(oh.Status.Phase).To(Equal(schedulingv1alpha1.PhaseAsleep))
		Expect(oh.Status.NextTransition.Time).To(BeTemporally("==", local(time.November, 2, 8, 0)))
	})

	It("keeps the schedule when someone scales up at night", func() {
		web := getDeployment(selectedNS, "web")
		base := web.DeepCopy()
		five := int32(5)
		web.Spec.Replicas = &five
		Expect(k8sClient.Patch(ctx, web, client.MergeFrom(base))).To(Succeed())

		reconcileSchedule(scheduleName)

		web = getDeployment(selectedNS, "web")
		Expect(*web.Spec.Replicas).To(BeEquivalentTo(0))
		Expect(web.Annotations).To(HaveKeyWithValue(annOriginalReplicas, "2")) // the morning value is kept
	})

	It("restores the original replicas in the morning", func() {
		fakeClock.SetTime(local(time.November, 2, 8, 0))
		reconcileSchedule(scheduleName)

		web := getDeployment(selectedNS, "web")
		Expect(*web.Spec.Replicas).To(BeEquivalentTo(2))
		Expect(web.Annotations).NotTo(HaveKey(annOriginalReplicas))
		Expect(web.Annotations).NotTo(HaveKey(annManagedBy))
		Expect(getSchedule(scheduleName).Status.Phase).To(Equal(schedulingv1alpha1.PhaseAwake))
	})

	It("wakes everything up when the schedule is deleted at night", func() {
		fakeClock.SetTime(local(time.November, 2, 20, 0))
		reconcileSchedule(scheduleName)
		Expect(*getDeployment(selectedNS, "web").Spec.Replicas).To(BeEquivalentTo(0))

		Expect(k8sClient.Delete(ctx, getSchedule(scheduleName))).To(Succeed())
		reconcileSchedule(scheduleName)

		Expect(*getDeployment(selectedNS, "web").Spec.Replicas).To(BeEquivalentTo(2))
		err := k8sClient.Get(ctx, types.NamespacedName{Name: scheduleName}, &schedulingv1alpha1.OfficeHours{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("reports a time zone that does not exist", func() {
		// "Asia/Jerusalim" passes the schema pattern but cannot be loaded.
		Expect(k8sClient.Create(ctx, newOfficeHours("bad-tz", "Asia/Jerusalim"))).To(Succeed())
		result := reconcileSchedule("bad-tz")

		ready := meta.FindStatusCondition(getSchedule("bad-tz").Status.Conditions, schedulingv1alpha1.ConditionReady)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(ready.Reason).To(Equal(reasonInvalidTimeZone))
		Expect(result.RequeueAfter).To(BeZero()) // retrying cannot fix the spec

		Expect(*getDeployment(selectedNS, "web").Spec.Replicas).To(BeEquivalentTo(2)) // fails open
	})
})
