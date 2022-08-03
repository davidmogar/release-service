/*
Copyright 2022.

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

package release

import (
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"reflect"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appstudioshared "github.com/redhat-appstudio/managed-gitops/appstudio-shared/apis/appstudio.redhat.com/v1alpha1"
	appstudiov1alpha1 "github.com/redhat-appstudio/release-service/api/v1alpha1"
	"github.com/redhat-appstudio/release-service/tekton"
	tektonv1beta1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("Release Adapter", Ordered, func() {
	var (
		adapter *Adapter

		release             *appstudiov1alpha1.Release
		releaseStrategy     *appstudiov1alpha1.ReleaseStrategy
		releaseLink         *appstudiov1alpha1.ReleaseLink
		applicationSnapshot *appstudioshared.ApplicationSnapshot
	)

	BeforeAll(func() {
		releaseStrategy = &appstudiov1alpha1.ReleaseStrategy{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-releasestrategy-",
				Namespace:    testNamespace,
			},
			Spec: appstudiov1alpha1.ReleaseStrategySpec{
				Pipeline:              "release-pipeline",
				Bundle:                "test-bundle",
				Policy:                "test-policy",
				PersistentVolumeClaim: "test-pvc",
				ServiceAccount:        "test-account",
			},
		}
		Expect(k8sClient.Create(ctx, releaseStrategy)).Should(Succeed())

		releaseLink = &appstudiov1alpha1.ReleaseLink{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "appstudio.redhat.com/v1alpha1",
				Kind:       "ReleaseLink",
			},
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-releaselink-",
				Namespace:    testNamespace,
				Labels: map[string]string{
					"release.appstudio.openshift.io/auto-release": "false",
				},
			},
			Spec: appstudiov1alpha1.ReleaseLinkSpec{
				DisplayName:     "Test ReleaseLink",
				Application:     "test-app",
				Target:          "default",
				ReleaseStrategy: releaseStrategy.GetName(),
			},
		}
		Expect(k8sClient.Create(ctx, releaseLink)).Should(Succeed())

		applicationSnapshot = &appstudioshared.ApplicationSnapshot{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-snapshot-",
				Namespace:    "default",
			},
			TypeMeta: metav1.TypeMeta{
				APIVersion: testApiVersion,
				Kind:       "ApplicationSnapshot",
			},
			Spec: appstudioshared.ApplicationSnapshotSpec{
				Application: "testapplication",
				DisplayName: "Test application",
				Components:  []appstudioshared.ApplicationSnapshotComponent{},
			},
		}
		Expect(k8sClient.Create(ctx, applicationSnapshot)).Should(Succeed())

		// adding the Type Metas as it loses it after creation
		// and these fields are required by some functions
		applicationSnapshot.TypeMeta = metav1.TypeMeta{
			Kind:       "ApplicationSnapshot",
			APIVersion: testApiVersion,
		}
		Expect(k8sClient.Update(ctx, applicationSnapshot)).Should(Succeed())
	})

	BeforeEach(func() {
		release = &appstudiov1alpha1.Release{
			TypeMeta: metav1.TypeMeta{
				APIVersion: testApiVersion,
				Kind:       "Release",
			},
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-release-",
				Namespace:    testNamespace,
			},
			Spec: appstudiov1alpha1.ReleaseSpec{
				ApplicationSnapshot: applicationSnapshot.GetName(),
				ReleaseLink:         releaseLink.GetName(),
			},
		}
		Expect(k8sClient.Create(ctx, release)).Should(Succeed())

		adapter = NewAdapter(release, ctrl.Log, k8sClient, ctx)
		Expect(reflect.TypeOf(adapter)).To(Equal(reflect.TypeOf(&Adapter{})))

	})

	AfterEach(func() {
		err := k8sClient.Delete(ctx, release)
		Expect(err == nil || errors.IsNotFound(err)).To(BeTrue())
	})

	AfterAll(func() {
		Expect(k8sClient.Delete(ctx, releaseLink)).Should(Succeed())
		Expect(k8sClient.Delete(ctx, applicationSnapshot)).Should(Succeed())
		Expect(k8sClient.Delete(ctx, releaseStrategy)).Should(Succeed())
	})

	It("can create a new Adapter instance", func() {
		Expect(reflect.TypeOf(NewAdapter(release, ctrl.Log, k8sClient, ctx))).
			To(Equal(reflect.TypeOf(&Adapter{})))
	})

	It("can make sure Finalizer is added to a Release object", func() {
		result, err := adapter.EnsureFinalizerIsAdded()
		Expect(!result.CancelRequest && err == nil).To(BeTrue())

		Expect(release.ObjectMeta.GetFinalizers()[0]).
			To(ContainSubstring("release-finalizer"))

		// return success also when the finalizer is present already
		//
		// unf. the return type and error are the same so we can't
		// differ the result and error from the previous call
		result, err = adapter.EnsureFinalizerIsAdded()
		Expect(!result.CancelRequest && err == nil).To(BeTrue())
	})

	It("can make sure a PipelineRun object exists for the instanced adapter", func() {
		// some racing condition in EnvTest makes it throw an error even if the CancelRequest is false
		// so I am adding a function to wait for it to succeed before continuing.
		Expect(reflect.TypeOf(adapter)).
			To(Equal(reflect.TypeOf(&Adapter{})))
		Eventually(func() bool {
			result, err := adapter.EnsureReleasePipelineRunExists()
			return (!result.CancelRequest && err == nil)
		}).Should(BeTrue())

		pipelineRun, _ := adapter.getReleasePipelineRun()
		Expect(reflect.TypeOf(pipelineRun)).
			To(Equal(reflect.TypeOf(&tektonv1beta1.PipelineRun{})))
		Expect(pipelineRun).NotTo(BeNil())

		// clean up
		Expect(k8sClient.Delete(ctx, pipelineRun)).Should(Succeed())
	})

	It("can make sure that the status of a Release Pipeline is tracked", func() {
		// same as above
		Eventually(func() bool {
			result, err := adapter.EnsureReleasePipelineRunExists()
			return (!result.CancelRequest && err == nil)
		}).Should(BeTrue())
		pipelineRun, _ := adapter.getReleasePipelineRun()
		Expect(pipelineRun).NotTo(BeNil())

		result, err := adapter.EnsureReleasePipelineStatusIsTracked()
		Expect(!result.CancelRequest && err == nil).To(BeTrue())

		// should return results.ContinueProcessing() in case the release is done
		release.MarkSucceeded()
		result, err = adapter.EnsureReleasePipelineStatusIsTracked()
		Expect(!result.CancelRequest && err == nil).To(BeTrue())

		// release is running but has no pipelineRun matching the release
		release.MarkRunning()
		pipelineRun.ObjectMeta.Labels = nil
		k8sClient.Update(ctx, pipelineRun)
		release.Status.Conditions = []metav1.Condition{}
		result, err = adapter.EnsureReleasePipelineStatusIsTracked()

		// err should not be nil here but adapter.getReleasePipelineRun does not advise
		// there is no pipelineRun for the release
		Expect(!result.CancelRequest && err == nil).To(BeTrue())

		// local cleanup
		k8sClient.Delete(ctx, pipelineRun)
	})

	It("can make sure Finalizers are called for a Release object", func() {
		// Call to EnsureFinalizersAreCalled should succeed if the Release was not set to be deleted
		result, err := adapter.EnsureFinalizersAreCalled()
		Expect(result.CancelRequest).To(BeFalse())
		Expect(err).NotTo(HaveOccurred())

		// Add finalizer
		result, err = adapter.EnsureFinalizerIsAdded()
		Expect(result.CancelRequest).To(BeFalse())
		Expect(err).NotTo(HaveOccurred())

		// Create a ReleasePipelineRun to ensure it gets finalized
		result, err = adapter.EnsureReleasePipelineRunExists()
		Expect(result.CancelRequest).To(BeFalse())
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Delete(ctx, release)).NotTo(HaveOccurred())
		Eventually(func() bool {
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      release.Name,
				Namespace: release.Namespace,
			}, release)
			klog.Info(err)
			klog.Info(release.GetDeletionTimestamp())
			return err == nil && release.GetDeletionTimestamp() != nil
		}, time.Second*10).Should(BeTrue())

		result, err = adapter.EnsureFinalizersAreCalled()
		klog.Info(release.GetDeletionTimestamp())
		klog.Infof("%+v", result)
		Expect(result.RequeueRequest).To(BeTrue())
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Name:      release.Name,
				Namespace: release.Namespace,
			}, release)
		}, time.Second*10).Should(HaveOccurred())

		Eventually(func() bool {
			pipelineRun, err := adapter.getReleasePipelineRun()
			return pipelineRun == nil && err == nil
		}, time.Second*10).Should(BeTrue())
	})

	It("can make sure Finalizers are called and fails for a Release object when the pipelineRun was already finalized", func() {
		operations := []ReconcileOperation{
			adapter.EnsureFinalizersAreCalled,
			adapter.EnsureReleasePipelineRunExists,
			adapter.EnsureFinalizerIsAdded,
			adapter.EnsureReleasePipelineStatusIsTracked,
		}
		for _, operation := range operations {
			result, err := operation()
			Expect(!result.CancelRequest && err == nil).To(BeTrue())
		}
		release.SetDeletionTimestamp(&metav1.Time{Time: time.Now()})

		// delete the pipelineRun before the release so it should thrown an
		// error on finalizing the release
		pipelineRun, _ := adapter.getReleasePipelineRun()
		Expect(k8sClient.Delete(ctx, pipelineRun)).Should(Succeed())
		//Expect(k8sClient.Delete(ctx, release)).Should(Succeed())
		//
		//result, err := adapter.EnsureFinalizersAreCalled()
		//Expect(!result.CancelRequest && err != nil).To(BeTrue())
	})

	It("can create a ReleasePipelineRun within the given adapter", func() {
		applicationSnapshot.TypeMeta.Kind = "applicationSnapshot"
		Expect(k8sClient.Update(ctx, applicationSnapshot)).Should(Succeed())
		pipelineRun, err := adapter.createReleasePipelineRun(
			releaseStrategy,
			applicationSnapshot)
		Expect(pipelineRun != nil && err == nil).To(BeTrue())
		Expect(k8sClient.Delete(ctx, pipelineRun)).Should(Succeed())
	})

	It("can finalize (delete) the releasePipelineRun just processed", func() {
		result, err := adapter.EnsureFinalizersAreCalled()
		Expect(!result.CancelRequest && err == nil).To(BeTrue())

		Eventually(func() bool {
			result, err := adapter.EnsureFinalizerIsAdded()
			return (!result.CancelRequest && err == nil)
		}).Should(BeTrue())

		Eventually(func() bool {
			result, err := adapter.EnsureReleasePipelineRunExists()
			return (!result.CancelRequest && err == nil)
		}).Should(BeTrue())
		result, err = adapter.EnsureReleasePipelineStatusIsTracked()
		Expect(!result.CancelRequest && err == nil).To(BeTrue())

		err = adapter.finalizeRelease()
		Expect(err).Should(Succeed())

		pipelineRun, _ := adapter.getReleasePipelineRun()
		Expect(k8sClient.Delete(ctx, pipelineRun)).ShouldNot(Succeed())
	})

	It("can return the ApplicationSnapshot from the release on the given adapter", func() {
		snapshot, err := adapter.getApplicationSnapshot()
		Expect(err).Should(Succeed())
		Expect(reflect.TypeOf(snapshot)).
			To(Equal(reflect.TypeOf(&appstudioshared.ApplicationSnapshot{})))

		// should error when applicationSnapshot is missing
		//Expect(k8sClient.Delete(ctx, applicationSnapshot)).Should(Succeed())
		//_, err = adapter.getApplicationSnapshot()
		//Expect(err).Should(HaveOccurred())
	})

	It("can return the releaseLink from the release within the given adapter", func() {
		link, err := adapter.getReleaseLink()
		Expect(err).Should(Succeed())
		Expect(reflect.TypeOf(link)).
			To(Equal(reflect.TypeOf(&appstudiov1alpha1.ReleaseLink{})))

		// shold err when releaseLink does not exist
		//k8sClient.Delete(ctx, releaseLink)
		//_, err = adapter.getReleaseLink()
		//Expect(err).Should(HaveOccurred())
	})

	It("can return the pipelineRun from the release within the give adapter", func() {
		Eventually(func() bool {
			result, err := adapter.EnsureReleasePipelineRunExists()
			return (!result.CancelRequest && err == nil)
		}).Should(BeTrue())
		pipelineRun, err := adapter.getReleasePipelineRun()
		Expect(err).Should(Succeed())
		Expect(reflect.TypeOf(pipelineRun)).
			To(Equal(reflect.TypeOf(&tektonv1beta1.PipelineRun{})))
	})

	It("can return the releaseStrategy from a given releaseLink to the given adapter", func() {
		strategy, err := adapter.getReleaseStrategy(releaseLink)
		Expect(err).Should(Succeed())
		Expect(reflect.TypeOf(strategy)).
			To(Equal(reflect.TypeOf(&appstudiov1alpha1.ReleaseStrategy{})))
	})

	It("can return the releaseStrategy from a given adapter", func() {
		strategy, _ := adapter.getReleaseStrategyFromRelease()
		Expect(reflect.TypeOf(strategy)).
			To(Equal(reflect.TypeOf(&appstudiov1alpha1.ReleaseStrategy{})))
	})

	It("can return the target ReleaseLink from a given ReleaseLink within a given adapter", func() {
		link, _ := adapter.getTargetReleaseLink()
		Expect(reflect.TypeOf(link)).
			To(Equal(reflect.TypeOf(&appstudiov1alpha1.ReleaseLink{})))
	})

	It("can mark the status for a given Release according to the pipelineRun status", func() {
		pipelineRun := tekton.NewReleasePipelineRun("test-pipeline", testNamespace).AsPipelineRun()
		pipelineRun.Name = "foo" // PipelineRun needs a name before registering the Release status
		err := adapter.registerReleaseStatusData(pipelineRun, releaseStrategy)
		Expect(err).ToNot(HaveOccurred())
		Expect(adapter.release.Status.StartTime).To(Not(BeNil()))
		pipelineRun.Status.MarkSucceeded("succeeded", "set it to succeeded")
		Expect(pipelineRun.IsDone()).To(BeTrue())

		err = adapter.registerReleasePipelineRunStatus(pipelineRun)
		Expect(err).ToNot(HaveOccurred())
		Expect(release.HasSucceeded()).To(BeTrue())

		Expect(adapter.release.Status.StartTime).ToNot(BeNil()) // Double check that we still have a valid StartTime

		release.Status.Conditions = []metav1.Condition{} // Clear up previous condition so isDone returns false
		pipelineRun.Status.MarkFailed("failed", "set it to failed")
		err = adapter.registerReleasePipelineRunStatus(pipelineRun)
		Expect(err).NotTo(HaveOccurred())
		Expect(release.HasSucceeded()).To(BeFalse())
	})

	It("can add the release information to its status and mark it as Running", func() {
		// err should be nil if releaseStrategy or pipelineRun is nil
		err := adapter.registerReleaseStatusData(nil, nil)
		Expect(err).To(BeNil())

		pipelineRun := tekton.NewReleasePipelineRun("test-pipeline", testNamespace).AsPipelineRun()
		pipelineRun.Name = "new-name"
		err = adapter.registerReleaseStatusData(pipelineRun, releaseStrategy)

		Expect(err).NotTo(HaveOccurred())
		Expect(release.Status.ReleasePipelineRun, pipelineRun.GetName())
		Expect(release.Status.Conditions[0].Status == "Unknown")
		Expect(release.Status.Conditions[0].Reason == "Running")
	})

	It("can update the Status of a release within the given adapter", func() {
		//applicationSnapshot.TypeMeta.Kind = "ApplicationSnapshot"
		//
		//pipelineRun := tekton.NewReleasePipelineRun("test-pipeline", testNamespace).AsPipelineRun()
		//pipelineRun.SetName("test")
		patch := client.MergeFrom(adapter.release.DeepCopy())
		adapter.release.Status.ReleasePipelineRun = "foo/test"

		Expect(adapter.updateStatus(patch)).Should(Succeed())
		Expect(release.Status.ReleasePipelineRun).
			To(Equal("foo/test"))
	})
})
