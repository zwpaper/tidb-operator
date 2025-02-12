// Copyright 2018 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package member

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	"github.com/pingcap/tidb-operator/pkg/apis/label"
	"github.com/pingcap/tidb-operator/pkg/apis/pingcap/v1alpha1"
	"github.com/pingcap/tidb-operator/pkg/controller"
	"github.com/pingcap/tidb-operator/pkg/pdapi"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/pointer"
)

func TestPDFailoverFailover(t *testing.T) {
	g := NewGomegaWithT(t)

	recorder := record.NewFakeRecorder(100)
	type testcase struct {
		name                     string
		update                   func(*v1alpha1.TidbCluster)
		maxFailoverCount         int32
		hasPVC                   bool
		hasPod                   bool
		podWithDeletionTimestamp bool
		pvcWithDeletionTimestamp bool
		delMemberFailed          bool
		delPodFailed             bool
		delPVCFailed             bool
		statusSyncFailed         bool
		errExpectFn              func(*GomegaWithT, error)
		expectFn                 func(*v1alpha1.TidbCluster, *pdFailover)
	}

	tests := []testcase{
		{
			name:                     "all members are ready",
			update:                   allMembersReady,
			maxFailoverCount:         3,
			hasPVC:                   true,
			hasPod:                   true,
			podWithDeletionTimestamp: false,
			delMemberFailed:          false,
			delPodFailed:             false,
			delPVCFailed:             false,
			statusSyncFailed:         false,
			errExpectFn:              errExpectNil,
			expectFn: func(tc *v1alpha1.TidbCluster, _ *pdFailover) {
				g.Expect(int(tc.Spec.PD.Replicas)).To(Equal(3))
				g.Expect(len(tc.Status.PD.FailureMembers)).To(Equal(0))
				events := collectEvents(recorder.Events)
				g.Expect(events).To(HaveLen(0))
			},
		},
		{
			name:                     "pd status sync failed",
			update:                   allMembersReady,
			maxFailoverCount:         3,
			hasPVC:                   true,
			hasPod:                   true,
			podWithDeletionTimestamp: false,
			delMemberFailed:          false,
			delPodFailed:             false,
			delPVCFailed:             false,
			statusSyncFailed:         true,
			errExpectFn:              errExpectNotNil,
			expectFn: func(tc *v1alpha1.TidbCluster, _ *pdFailover) {
				g.Expect(int(tc.Spec.PD.Replicas)).To(Equal(3))
				events := collectEvents(recorder.Events)
				g.Expect(events).To(HaveLen(0))
			},
		},
		{
			name:                     "two members are not ready, not in quorum",
			update:                   twoMembersNotReady,
			maxFailoverCount:         3,
			hasPVC:                   true,
			hasPod:                   true,
			podWithDeletionTimestamp: false,
			delMemberFailed:          false,
			delPodFailed:             false,
			delPVCFailed:             false,
			statusSyncFailed:         false,
			errExpectFn: func(g *GomegaWithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(strings.Contains(err.Error(), "pd cluster is not health")).To(Equal(true))
			},
			expectFn: func(tc *v1alpha1.TidbCluster, _ *pdFailover) {
				g.Expect(int(tc.Spec.PD.Replicas)).To(Equal(3))
				g.Expect(len(tc.Status.PD.FailureMembers)).To(Equal(0))
				events := collectEvents(recorder.Events)
				sort.Strings(events)
				g.Expect(events).To(HaveLen(2))
				g.Expect(events[0]).To(ContainSubstring("test-pd-0(0) is unhealthy"))
				g.Expect(events[1]).To(ContainSubstring("test-pd-1(12891273174085095651) is unhealthy"))
			},
		},
		{
			name:                     "two members are not ready while two peermembers ready, cluster in quorum",
			update:                   twoMembersNotReadyWithPeerMembers,
			maxFailoverCount:         3,
			hasPVC:                   true,
			hasPod:                   true,
			podWithDeletionTimestamp: false,
			delMemberFailed:          false,
			delPodFailed:             false,
			delPVCFailed:             false,
			statusSyncFailed:         false,
			errExpectFn:              errExpectNil,
			expectFn: func(tc *v1alpha1.TidbCluster, _ *pdFailover) {
				g.Expect(int(tc.Spec.PD.Replicas)).To(Equal(3))
				g.Expect(len(tc.Status.PD.FailureMembers)).To(Equal(0))
				events := collectEvents(recorder.Events)
				sort.Strings(events)
				g.Expect(events).To(HaveLen(2))
				g.Expect(events[0]).To(ContainSubstring("test-pd-0(0) is unhealthy"))
				g.Expect(events[1]).To(ContainSubstring("test-pd-1(12891273174085095651) is unhealthy"))
			},
		},
		{
			name: "two members are not ready while two peermembers ready, cluster not in quorum",
			update: func(tc *v1alpha1.TidbCluster) {
				pd0 := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 0)
				pd1 := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 1)
				pd2 := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 2)
				tc.Status.PD.Members = map[string]v1alpha1.PDMember{
					pd0: {Name: pd0, ID: "0", Health: true},
					pd1: {Name: pd1, ID: "12891273174085095651", Health: false},
					pd2: {Name: pd2, ID: "2", Health: true},
				}
				tc.Status.PD.PeerMembers = map[string]v1alpha1.PDMember{
					pd0: {Name: pd0, ID: "0", Health: false},
					pd2: {Name: pd2, ID: "2", Health: true},
				}
			},
			maxFailoverCount:         3,
			hasPVC:                   true,
			hasPod:                   true,
			podWithDeletionTimestamp: false,
			delMemberFailed:          false,
			delPodFailed:             false,
			delPVCFailed:             false,
			statusSyncFailed:         false,
			errExpectFn:              errExpectNil,
			expectFn: func(tc *v1alpha1.TidbCluster, _ *pdFailover) {
				g.Expect(int(tc.Spec.PD.Replicas)).To(Equal(3))
				g.Expect(len(tc.Status.PD.FailureMembers)).To(Equal(0))
				events := collectEvents(recorder.Events)
				sort.Strings(events)
				g.Expect(events).To(HaveLen(2))
				g.Expect(events[0]).To(ContainSubstring("test-pd-1(12891273174085095651) is unhealthy"))
			},
		},
		{
			name:                     "two members are ready and a failure member",
			update:                   oneFailureMember,
			maxFailoverCount:         3,
			hasPVC:                   true,
			hasPod:                   true,
			podWithDeletionTimestamp: false,
			delMemberFailed:          false,
			delPodFailed:             false,
			delPVCFailed:             false,
			errExpectFn:              errExpectNil,
			expectFn: func(tc *v1alpha1.TidbCluster, _ *pdFailover) {
				g.Expect(int(tc.Spec.PD.Replicas)).To(Equal(3))
				g.Expect(len(tc.Status.PD.FailureMembers)).To(Equal(1))
				pd1Name := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 1)
				pd1, ok := tc.Status.PD.FailureMembers[pd1Name]
				g.Expect(ok).To(Equal(true))
				g.Expect(pd1.MemberDeleted).To(Equal(true))
				events := collectEvents(recorder.Events)
				g.Expect(events).To(HaveLen(1))
				g.Expect(events[0]).To(ContainSubstring("failure member default/test-pd-1(12891273174085095651) deleted from PD cluster"))
			},
		},
		{
			name: "has one not ready member, but not exceed deadline",
			update: func(tc *v1alpha1.TidbCluster) {
				oneNotReadyMember(tc)
				pd1Name := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 1)
				pd1 := tc.Status.PD.Members[pd1Name]
				pd1.LastTransitionTime = metav1.Time{Time: time.Now().Add(-2 * time.Minute)}
				tc.Status.PD.Members[pd1Name] = pd1
			},
			maxFailoverCount:         3,
			hasPVC:                   true,
			hasPod:                   true,
			podWithDeletionTimestamp: false,
			delMemberFailed:          false,
			delPodFailed:             false,
			delPVCFailed:             false,
			statusSyncFailed:         false,
			errExpectFn:              errExpectNil,
			expectFn: func(tc *v1alpha1.TidbCluster, _ *pdFailover) {
				g.Expect(int(tc.Spec.PD.Replicas)).To(Equal(3))
				g.Expect(len(tc.Status.PD.FailureMembers)).To(Equal(0))
				events := collectEvents(recorder.Events)
				g.Expect(events).To(HaveLen(1))
				g.Expect(events[0]).To(ContainSubstring("test-pd-1(12891273174085095651) is unhealthy"))
			},
		},
		{
			name: "has one not ready member, and exceed deadline, lastTransitionTime is zero",
			update: func(tc *v1alpha1.TidbCluster) {
				oneNotReadyMember(tc)
				pd1Name := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 1)
				pd1 := tc.Status.PD.Members[pd1Name]
				pd1.LastTransitionTime = metav1.Time{}
				tc.Status.PD.Members[pd1Name] = pd1
			},
			maxFailoverCount:         3,
			hasPVC:                   true,
			hasPod:                   true,
			podWithDeletionTimestamp: false,
			delMemberFailed:          false,
			delPodFailed:             false,
			delPVCFailed:             false,
			statusSyncFailed:         false,
			errExpectFn:              errExpectNil,
			expectFn: func(tc *v1alpha1.TidbCluster, _ *pdFailover) {
				g.Expect(int(tc.Spec.PD.Replicas)).To(Equal(3))
				g.Expect(len(tc.Status.PD.FailureMembers)).To(Equal(0))
				events := collectEvents(recorder.Events)
				g.Expect(events).To(HaveLen(1))
				g.Expect(events[0]).To(ContainSubstring("test-pd-1(12891273174085095651) is unhealthy"))
			},
		},
		{
			name:                     "has one not ready member, don't have pvc",
			update:                   oneNotReadyMember,
			maxFailoverCount:         3,
			hasPVC:                   false,
			hasPod:                   true,
			podWithDeletionTimestamp: false,
			delMemberFailed:          false,
			delPodFailed:             false,
			delPVCFailed:             false,
			statusSyncFailed:         false,
			errExpectFn: func(g *GomegaWithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("no pvc found for pod default/test-pd-1"))
			},
			expectFn: func(tc *v1alpha1.TidbCluster, _ *pdFailover) {
				g.Expect(int(tc.Spec.PD.Replicas)).To(Equal(3))
				g.Expect(len(tc.Status.PD.FailureMembers)).To(Equal(0))
				events := collectEvents(recorder.Events)
				g.Expect(events).To(HaveLen(1))
				g.Expect(events[0]).To(ContainSubstring("test-pd-1(12891273174085095651) is unhealthy"))
			},
		},
		{
			name:                     "has one not ready member",
			update:                   oneNotReadyMember,
			maxFailoverCount:         3,
			hasPVC:                   true,
			hasPod:                   true,
			podWithDeletionTimestamp: false,
			delMemberFailed:          false,
			delPodFailed:             false,
			delPVCFailed:             false,
			statusSyncFailed:         false,
			errExpectFn: func(g *GomegaWithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(strings.Contains(err.Error(), "marking Pod: default/test-pd-1 pd member: test-pd-1 as failure")).To(Equal(true))
			},
			expectFn: func(tc *v1alpha1.TidbCluster, _ *pdFailover) {
				g.Expect(int(tc.Spec.PD.Replicas)).To(Equal(3))
				g.Expect(len(tc.Status.PD.FailureMembers)).To(Equal(1))
				failureMembers := tc.Status.PD.FailureMembers["test-pd-1"]
				g.Expect(failureMembers.PodName).To(Equal("test-pd-1"))
				g.Expect(failureMembers.MemberID).To(Equal("12891273174085095651"))
				g.Expect(string(failureMembers.PVCUID)).To(Equal(""))
				g.Expect(failureMembers.PVCUIDSet).To(HaveKey(types.UID("pvc-1-uid-1")))
				g.Expect(failureMembers.PVCUIDSet).To(HaveKey(types.UID("pvc-1-uid-2")))
				g.Expect(failureMembers.MemberDeleted).To(BeFalse())
				events := collectEvents(recorder.Events)
				g.Expect(events).To(HaveLen(2))
				g.Expect(events[0]).To(ContainSubstring("test-pd-1(12891273174085095651) is unhealthy"))
				g.Expect(events[1]).To(ContainSubstring("PDMemberUnhealthy default/test-pd-1(12891273174085095651) is unhealthy"))
			},
		},
		{
			name:                     "has one not ready member but maxFailoverCount is 0",
			update:                   oneNotReadyMember,
			maxFailoverCount:         0,
			hasPVC:                   true,
			hasPod:                   true,
			podWithDeletionTimestamp: false,
			delMemberFailed:          false,
			delPodFailed:             false,
			delPVCFailed:             false,
			statusSyncFailed:         false,
			errExpectFn:              errExpectNil,
			expectFn: func(tc *v1alpha1.TidbCluster, _ *pdFailover) {
				g.Expect(int(tc.Spec.PD.Replicas)).To(Equal(3))
				g.Expect(len(tc.Status.PD.FailureMembers)).To(Equal(0))
				events := collectEvents(recorder.Events)
				g.Expect(events).To(HaveLen(1))
				g.Expect(events[0]).To(ContainSubstring("test-pd-1(12891273174085095651) is unhealthy"))
			},
		},
		{
			name:                     "has one not ready member, and exceed deadline, don't have PVC, has Pod, delete pod success",
			update:                   oneNotReadyMemberAndAFailureMember,
			maxFailoverCount:         3,
			hasPVC:                   false,
			hasPod:                   true,
			podWithDeletionTimestamp: false,
			delMemberFailed:          false,
			delPodFailed:             false,
			delPVCFailed:             false,
			statusSyncFailed:         false,
			errExpectFn:              errExpectNil,
			expectFn: func(tc *v1alpha1.TidbCluster, _ *pdFailover) {
				g.Expect(int(tc.Spec.PD.Replicas)).To(Equal(3))
				pd1Name := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 1)
				pd1, ok := tc.Status.PD.FailureMembers[pd1Name]
				g.Expect(ok).To(Equal(true))
				g.Expect(pd1.MemberDeleted).To(Equal(true))
				events := collectEvents(recorder.Events)
				g.Expect(events).To(HaveLen(2))
				g.Expect(events[0]).To(ContainSubstring("test-pd-1(12891273174085095651) is unhealthy"))
				g.Expect(events[1]).To(ContainSubstring("failure member default/test-pd-1(12891273174085095651) deleted from PD cluster"))
			},
		},
		{
			name: "has one not ready member, and exceed deadline, don't have PVC, has Pod, delete pod success, but memberID is wrong",
			update: func(tc *v1alpha1.TidbCluster) {
				oneNotReadyMemberAndAFailureMember(tc)
				pd1Name := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 1)
				pd1 := tc.Status.PD.FailureMembers[pd1Name]
				pd1.MemberID = "wrong-id"
				tc.Status.PD.FailureMembers[pd1Name] = pd1
			},
			maxFailoverCount:         3,
			hasPVC:                   false,
			hasPod:                   true,
			podWithDeletionTimestamp: false,
			delMemberFailed:          false,
			delPodFailed:             false,
			delPVCFailed:             false,
			statusSyncFailed:         false,
			errExpectFn: func(g *GomegaWithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(strings.Contains(err.Error(), "invalid syntax")).To(Equal(true))
			},
			expectFn: func(tc *v1alpha1.TidbCluster, _ *pdFailover) {
				g.Expect(int(tc.Spec.PD.Replicas)).To(Equal(3))
				pd1Name := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 1)
				pd1, ok := tc.Status.PD.FailureMembers[pd1Name]
				g.Expect(ok).To(Equal(true))
				g.Expect(pd1.MemberDeleted).To(Equal(false))
				events := collectEvents(recorder.Events)
				g.Expect(events).To(HaveLen(1))
				g.Expect(events[0]).To(ContainSubstring("test-pd-1(12891273174085095651) is unhealthy"))
			},
		},
		{
			name:                     "has one not ready member, and exceed deadline, don't have PVC, has Pod, delete member failed",
			update:                   oneNotReadyMemberAndAFailureMember,
			maxFailoverCount:         3,
			hasPVC:                   false,
			hasPod:                   true,
			podWithDeletionTimestamp: false,
			delMemberFailed:          true,
			delPodFailed:             false,
			delPVCFailed:             false,
			statusSyncFailed:         false,
			errExpectFn: func(g *GomegaWithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(strings.Contains(err.Error(), "failed to delete member")).To(Equal(true))
			},
			expectFn: func(tc *v1alpha1.TidbCluster, _ *pdFailover) {
				g.Expect(int(tc.Spec.PD.Replicas)).To(Equal(3))
				pd1Name := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 1)
				pd1, ok := tc.Status.PD.FailureMembers[pd1Name]
				g.Expect(ok).To(Equal(true))
				g.Expect(pd1.MemberDeleted).To(Equal(false))
				events := collectEvents(recorder.Events)
				g.Expect(events).To(HaveLen(1))
				g.Expect(events[0]).To(ContainSubstring("test-pd-1(12891273174085095651) is unhealthy"))
			},
		},
		{
			name:                     "has one not ready member, and exceed deadline, don't have PVC, has Pod, delete pod failed",
			update:                   oneNotReadyMemberAndAFailureMember,
			maxFailoverCount:         3,
			hasPVC:                   false,
			hasPod:                   true,
			podWithDeletionTimestamp: false,
			delMemberFailed:          false,
			delPodFailed:             true,
			delPVCFailed:             false,
			statusSyncFailed:         false,
			errExpectFn: func(g *GomegaWithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(strings.Contains(err.Error(), "delete pod: API server failed")).To(Equal(true))
			},
			expectFn: func(tc *v1alpha1.TidbCluster, _ *pdFailover) {
				g.Expect(int(tc.Spec.PD.Replicas)).To(Equal(3))
				pd1Name := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 1)
				pd1, ok := tc.Status.PD.FailureMembers[pd1Name]
				g.Expect(ok).To(Equal(true))
				g.Expect(pd1.MemberDeleted).To(Equal(false))
				events := collectEvents(recorder.Events)
				g.Expect(events).To(HaveLen(2))
				g.Expect(events[0]).To(ContainSubstring("test-pd-1(12891273174085095651) is unhealthy"))
				g.Expect(events[1]).To(ContainSubstring("failure member default/test-pd-1(12891273174085095651) deleted from PD cluster"))
			},
		},
		{
			name:                     "has one not ready member, and exceed deadline, has Pod, delete pvc failed",
			update:                   oneNotReadyMemberAndAFailureMember,
			maxFailoverCount:         3,
			hasPVC:                   true,
			hasPod:                   true,
			podWithDeletionTimestamp: false,
			delMemberFailed:          false,
			delPodFailed:             false,
			delPVCFailed:             true,
			statusSyncFailed:         false,
			errExpectFn: func(g *GomegaWithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(strings.Contains(err.Error(), "delete pvc: API server failed")).To(Equal(true))
			},
			expectFn: func(tc *v1alpha1.TidbCluster, _ *pdFailover) {
				g.Expect(int(tc.Spec.PD.Replicas)).To(Equal(3))
				pd1Name := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 1)
				pd1, ok := tc.Status.PD.FailureMembers[pd1Name]
				g.Expect(ok).To(Equal(true))
				g.Expect(pd1.MemberDeleted).To(Equal(false))
				events := collectEvents(recorder.Events)
				g.Expect(events).To(HaveLen(2))
				g.Expect(events[0]).To(ContainSubstring("test-pd-1(12891273174085095651) is unhealthy"))
				g.Expect(events[1]).To(ContainSubstring("failure member default/test-pd-1(12891273174085095651) deleted from PD cluster"))
			},
		},
		{
			name:                     "has one not ready member, and exceed deadline, has Pod with deletion timestamp",
			update:                   oneNotReadyMemberAndAFailureMember,
			maxFailoverCount:         3,
			hasPVC:                   true,
			hasPod:                   true,
			podWithDeletionTimestamp: true,
			pvcWithDeletionTimestamp: false,
			delMemberFailed:          false,
			delPodFailed:             false,
			delPVCFailed:             false,
			statusSyncFailed:         false,
			errExpectFn:              errExpectNil,
			expectFn: func(tc *v1alpha1.TidbCluster, pf *pdFailover) {
				g.Expect(int(tc.Spec.PD.Replicas)).To(Equal(3))
				pd1Name := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 1)
				pvcName := ordinalPVCName(v1alpha1.PDMemberType, controller.PDMemberName(tc.GetName()), 1)
				pd1, ok := tc.Status.PD.FailureMembers[pd1Name]
				g.Expect(ok).To(Equal(true))
				g.Expect(pd1.MemberDeleted).To(Equal(true))
				_, err := pf.deps.PodLister.Pods(metav1.NamespaceDefault).Get(pd1Name)
				g.Expect(err).NotTo(HaveOccurred())
				_, err = pf.deps.PVCLister.PersistentVolumeClaims(metav1.NamespaceDefault).Get(pvcName + "-1")
				g.Expect(err).To(HaveOccurred())
				g.Expect(errors.IsNotFound(err)).To(BeTrue())
				_, err = pf.deps.PVCLister.PersistentVolumeClaims(metav1.NamespaceDefault).Get(pvcName + "-2")
				g.Expect(err).To(HaveOccurred())
				g.Expect(errors.IsNotFound(err)).To(BeTrue())
				events := collectEvents(recorder.Events)
				g.Expect(events).To(HaveLen(2))
				g.Expect(events[0]).To(ContainSubstring("test-pd-1(12891273174085095651) is unhealthy"))
				g.Expect(events[1]).To(ContainSubstring("failure member default/test-pd-1(12891273174085095651) deleted from PD cluster"))
			},
		},
		{
			name:                     "has one not ready member, and exceed deadline, has PVC with deletion timestamp",
			update:                   oneNotReadyMemberAndAFailureMember,
			maxFailoverCount:         3,
			hasPVC:                   true,
			hasPod:                   true,
			podWithDeletionTimestamp: false,
			pvcWithDeletionTimestamp: true,
			delMemberFailed:          false,
			delPodFailed:             false,
			delPVCFailed:             false,
			statusSyncFailed:         false,
			errExpectFn:              errExpectNil,
			expectFn: func(tc *v1alpha1.TidbCluster, pf *pdFailover) {
				g.Expect(int(tc.Spec.PD.Replicas)).To(Equal(3))
				pd1Name := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 1)
				pvcName := ordinalPVCName(v1alpha1.PDMemberType, controller.PDMemberName(tc.GetName()), 1)
				pd1, ok := tc.Status.PD.FailureMembers[pd1Name]
				g.Expect(ok).To(Equal(true))
				g.Expect(pd1.MemberDeleted).To(Equal(true))
				_, err := pf.deps.PodLister.Pods(metav1.NamespaceDefault).Get(pd1Name)
				g.Expect(err).To(HaveOccurred())
				g.Expect(errors.IsNotFound(err)).To(BeTrue())
				_, err = pf.deps.PVCLister.PersistentVolumeClaims(metav1.NamespaceDefault).Get(pvcName + "-1")
				g.Expect(err).NotTo(HaveOccurred())
				_, err = pf.deps.PVCLister.PersistentVolumeClaims(metav1.NamespaceDefault).Get(pvcName + "-2")
				g.Expect(err).NotTo(HaveOccurred())
				events := collectEvents(recorder.Events)
				g.Expect(events).To(HaveLen(2))
				g.Expect(events[0]).To(ContainSubstring("test-pd-1(12891273174085095651) is unhealthy"))
				g.Expect(events[1]).To(ContainSubstring("failure member default/test-pd-1(12891273174085095651) deleted from PD cluster"))
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tc := newTidbClusterForPD()
			tc.Spec.PD.MaxFailoverCount = pointer.Int32Ptr(test.maxFailoverCount)
			test.update(tc)

			pdFailover, pvcIndexer, podIndexer, fakePDControl, fakePodControl, fakePVCControl := newFakePDFailover()
			pdFailover.deps.Recorder = recorder
			pdClient := controller.NewFakePDClient(fakePDControl, tc)

			pdClient.AddReaction(pdapi.DeleteMemberByIDActionType, func(action *pdapi.Action) (interface{}, error) {
				if test.delMemberFailed {
					return nil, fmt.Errorf("failed to delete member")
				}
				return nil, nil
			})

			var pvc1 *corev1.PersistentVolumeClaim
			var pvc2 *corev1.PersistentVolumeClaim
			if test.hasPVC {
				pvc1 = newPVCForPDFailover(tc, v1alpha1.PDMemberType, 1)
				pvc2 = pvc1.DeepCopy()
				pvc1.Name = pvc1.Name + "-1"
				pvc1.UID = pvc1.UID + "-1"
				pvc2.Name = pvc2.Name + "-2"
				pvc2.UID = pvc2.UID + "-2"

				if test.pvcWithDeletionTimestamp {
					pvc1.DeletionTimestamp = &metav1.Time{Time: time.Now()}
					pvc2.DeletionTimestamp = &metav1.Time{Time: time.Now()}
				}
				pvcIndexer.Add(pvc1)
				pvcIndexer.Add(pvc2)
			}
			// TODO: all test cases hasPod==true, should we remove this?
			if test.hasPod {
				pod := newPodForPDFailover(tc, v1alpha1.PDMemberType, 1)
				if test.podWithDeletionTimestamp {
					pod.DeletionTimestamp = &metav1.Time{Time: time.Now()}
				}
				if test.hasPVC {
					pvc1.ObjectMeta.Labels[label.AnnPodNameKey] = pod.GetName()
					pvc2.ObjectMeta.Labels[label.AnnPodNameKey] = pod.GetName()
					pod.Spec.Volumes = append(pod.Spec.Volumes,
						corev1.Volume{
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: pvc1.Name,
								},
							},
						},
						corev1.Volume{
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: pvc2.Name,
								},
							},
						})
				}
				podIndexer.Add(pod)
			}
			if test.delPodFailed {
				fakePodControl.SetDeletePodError(errors.NewInternalError(fmt.Errorf("delete pod: API server failed")), 0)
			}
			if test.delPVCFailed {
				fakePVCControl.SetDeletePVCError(errors.NewInternalError(fmt.Errorf("delete pvc: API server failed")), 0)
			}

			tc.Status.PD.Synced = !test.statusSyncFailed

			err := pdFailover.Failover(tc)
			test.errExpectFn(g, err)
			test.expectFn(tc, pdFailover)
		})
	}
}

func TestPDFailoverRecovery(t *testing.T) {
	g := NewGomegaWithT(t)

	type testcase struct {
		name     string
		update   func(*v1alpha1.TidbCluster)
		expectFn func(*v1alpha1.TidbCluster)
	}
	testFn := func(test *testcase, t *testing.T) {
		t.Log(test.name)
		tc := newTidbClusterForPD()
		test.update(tc)

		pdFailover, _, _, _, _, _ := newFakePDFailover()
		pdFailover.Recover(tc)
		test.expectFn(tc)
	}
	tests := []testcase{
		{
			name: "two failure member, user don't modify the replicas",
			update: func(tc *v1alpha1.TidbCluster) {
				twoFailureMembers(tc)
				tc.Spec.PD.Replicas = 3
			},
			expectFn: func(tc *v1alpha1.TidbCluster) {
				g.Expect(int(tc.Spec.PD.Replicas)).To(Equal(3))
				g.Expect(len(tc.Status.PD.FailureMembers)).To(Equal(0))
			},
		},
		{
			name: "two failure member, user modify the replicas to 4",
			update: func(tc *v1alpha1.TidbCluster) {
				twoFailureMembers(tc)
				tc.Spec.PD.Replicas = 4
			},
			expectFn: func(tc *v1alpha1.TidbCluster) {
				g.Expect(int(tc.Spec.PD.Replicas)).To(Equal(4))
				g.Expect(len(tc.Status.PD.FailureMembers)).To(Equal(0))
			},
		},
		{
			name: "two failure member, user increase the replicas",
			update: func(tc *v1alpha1.TidbCluster) {
				twoFailureMembers(tc)
				tc.Spec.PD.Replicas = 7
			},
			expectFn: func(tc *v1alpha1.TidbCluster) {
				g.Expect(int(tc.Spec.PD.Replicas)).To(Equal(7))
				g.Expect(len(tc.Status.PD.FailureMembers)).To(Equal(0))
			},
		},
		{
			name: "two failure member, user decrease the replicas",
			update: func(tc *v1alpha1.TidbCluster) {
				twoFailureMembers(tc)
				tc.Spec.PD.Replicas = 1
			},
			expectFn: func(tc *v1alpha1.TidbCluster) {
				g.Expect(int(tc.Spec.PD.Replicas)).To(Equal(1))
				g.Expect(len(tc.Status.PD.FailureMembers)).To(Equal(0))
			},
		},
		{
			name: "one failure member, user don't modify the replicas",
			update: func(tc *v1alpha1.TidbCluster) {
				oneFailureMember(tc)
				tc.Spec.PD.Replicas = 3
			},
			expectFn: func(tc *v1alpha1.TidbCluster) {
				g.Expect(int(tc.Spec.PD.Replicas)).To(Equal(3))
				g.Expect(len(tc.Status.PD.FailureMembers)).To(Equal(0))
			},
		},
		{
			name: "two failure member, user increase the replicas",
			update: func(tc *v1alpha1.TidbCluster) {
				oneFailureMember(tc)
				tc.Spec.PD.Replicas = 5
			},
			expectFn: func(tc *v1alpha1.TidbCluster) {
				g.Expect(int(tc.Spec.PD.Replicas)).To(Equal(5))
				g.Expect(len(tc.Status.PD.FailureMembers)).To(Equal(0))
			},
		},
		{
			name: "two failure member, user decrease the replicas",
			update: func(tc *v1alpha1.TidbCluster) {
				oneFailureMember(tc)
				tc.Spec.PD.Replicas = 1
			},
			expectFn: func(tc *v1alpha1.TidbCluster) {
				g.Expect(int(tc.Spec.PD.Replicas)).To(Equal(1))
				g.Expect(len(tc.Status.PD.FailureMembers)).To(Equal(0))
			},
		},
	}

	for i := range tests {
		testFn(&tests[i], t)
	}
}

func newFakePDFailover() (*pdFailover, cache.Indexer, cache.Indexer, *pdapi.FakePDControl, *controller.FakePodControl, *controller.FakePVCControl) {
	fakeDeps := controller.NewFakeDependencies()
	pdFailover := &pdFailover{deps: fakeDeps}
	pvcIndexer := fakeDeps.KubeInformerFactory.Core().V1().PersistentVolumeClaims().Informer().GetIndexer()
	podIndexer := fakeDeps.KubeInformerFactory.Core().V1().Pods().Informer().GetIndexer()
	pdControl := fakeDeps.PDControl.(*pdapi.FakePDControl)
	podControl := fakeDeps.PodControl.(*controller.FakePodControl)
	pvcControl := fakeDeps.PVCControl.(*controller.FakePVCControl)
	return pdFailover, pvcIndexer, podIndexer, pdControl, podControl, pvcControl
}

func oneFailureMember(tc *v1alpha1.TidbCluster) {
	pd0 := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 0)
	pd1 := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 1)
	pd2 := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 2)
	tc.Status.PD.Members = map[string]v1alpha1.PDMember{
		pd0: {Name: pd0, ID: "0", Health: true},
		pd2: {Name: pd2, ID: "2", Health: true},
	}

	pvcUIDSet := make(map[types.UID]struct{})
	pvcUIDSet[types.UID("pvc-1-uid-1")] = struct{}{}
	pvcUIDSet[types.UID("pvc-1-uid-2")] = struct{}{}
	tc.Status.PD.FailureMembers = map[string]v1alpha1.PDFailureMember{
		pd1: {PodName: pd1, PVCUIDSet: pvcUIDSet, MemberID: "12891273174085095651"},
	}
}

func twoFailureMembers(tc *v1alpha1.TidbCluster) {
	pd0 := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 0)
	pd1 := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 1)
	pd2 := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 2)
	tc.Status.PD.Members = map[string]v1alpha1.PDMember{
		pd2: {Name: pd2, ID: "2", Health: true},
	}
	tc.Status.PD.FailureMembers = map[string]v1alpha1.PDFailureMember{
		pd0: {PodName: pd0},
		pd1: {PodName: pd1},
	}
}

func oneNotReadyMember(tc *v1alpha1.TidbCluster) {
	pd0 := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 0)
	pd1 := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 1)
	pd2 := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 2)
	tc.Status.PD.Members = map[string]v1alpha1.PDMember{
		pd0: {Name: pd0, ID: "0", Health: true},
		pd1: {Name: pd1, ID: "12891273174085095651", Health: false, LastTransitionTime: metav1.Time{Time: time.Now().Add(-10 * time.Minute)}},
		pd2: {Name: pd2, ID: "2", Health: true},
	}
}

func oneNotReadyMemberAndAFailureMember(tc *v1alpha1.TidbCluster) {
	pd0 := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 0)
	pd1 := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 1)
	pd2 := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 2)
	tc.Status.PD.Members = map[string]v1alpha1.PDMember{
		pd0: {Name: pd0, ID: "0", Health: true},
		pd1: {Name: pd1, ID: "12891273174085095651", Health: false, LastTransitionTime: metav1.Time{Time: time.Now().Add(-10 * time.Minute)}},
		pd2: {Name: pd2, ID: "2", Health: true},
	}

	pvcUIDSet := make(map[types.UID]struct{})
	pvcUIDSet[types.UID("pvc-1-uid-1")] = struct{}{}
	pvcUIDSet[types.UID("pvc-1-uid-2")] = struct{}{}
	tc.Status.PD.FailureMembers = map[string]v1alpha1.PDFailureMember{
		pd1: {PodName: pd1, PVCUIDSet: pvcUIDSet, MemberID: "12891273174085095651"},
	}
}

func allMembersReady(tc *v1alpha1.TidbCluster) {
	pd0 := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 0)
	pd1 := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 1)
	pd2 := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 2)
	tc.Status.PD.Members = map[string]v1alpha1.PDMember{
		pd0: {Name: pd0, ID: "0", Health: true},
		pd1: {Name: pd1, ID: "12891273174085095651", Health: true},
		pd2: {Name: pd2, ID: "2", Health: true},
	}
}

func twoMembersNotReady(tc *v1alpha1.TidbCluster) {
	pd0 := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 0)
	pd1 := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 1)
	pd2 := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 2)
	tc.Status.PD.Members = map[string]v1alpha1.PDMember{
		pd0: {Name: pd0, ID: "0", Health: false},
		pd1: {Name: pd1, ID: "12891273174085095651", Health: false},
		pd2: {Name: pd2, ID: "2", Health: true},
	}
}

func twoMembersNotReadyWithPeerMembers(tc *v1alpha1.TidbCluster) {
	pd0 := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 0)
	pd1 := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 1)
	pd2 := ordinalPodName(v1alpha1.PDMemberType, tc.GetName(), 2)
	tc.Status.PD.Members = map[string]v1alpha1.PDMember{
		pd0: {Name: pd0, ID: "0", Health: false},
		pd1: {Name: pd1, ID: "12891273174085095651", Health: false},
		pd2: {Name: pd2, ID: "2", Health: true},
	}
	tc.Status.PD.PeerMembers = map[string]v1alpha1.PDMember{
		pd0: {Name: pd0, ID: "0", Health: true},
		pd2: {Name: pd2, ID: "2", Health: true},
	}
}

func errExpectNil(g *GomegaWithT, err error) {
	g.Expect(err).NotTo(HaveOccurred())
}

func errExpectNotNil(g *GomegaWithT, err error) {
	g.Expect(err).To(HaveOccurred())
}

func newPVCForPDFailover(tc *v1alpha1.TidbCluster, memberType v1alpha1.MemberType, ordinal int32) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ordinalPVCName(memberType, controller.PDMemberName(tc.GetName()), ordinal),
			Namespace: metav1.NamespaceDefault,
			UID:       types.UID("pvc-1-uid"),
			Labels: map[string]string{
				label.NameLabelKey:      "tidb-cluster",
				label.ManagedByLabelKey: label.TiDBOperator,
				label.InstanceLabelKey:  "test",
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			VolumeName: fmt.Sprintf("pv-%d", ordinal),
		},
	}
}

func newPodForPDFailover(tc *v1alpha1.TidbCluster, memberType v1alpha1.MemberType, ordinal int32) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ordinalPodName(memberType, tc.GetName(), ordinal),
			Namespace: metav1.NamespaceDefault,
		},
	}
}

func collectEvents(source <-chan string) []string {
	done := false
	events := make([]string, 0)
	for !done {
		select {
		case event := <-source:
			events = append(events, event)
		default:
			done = true
		}
	}
	return events
}
