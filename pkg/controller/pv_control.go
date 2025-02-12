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

package controller

import (
	"context"
	"fmt"
	"strings"

	"github.com/pingcap/tidb-operator/pkg/apis/label"
	corev1 "k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog"
)

// PVControlInterface manages PVs used in TidbCluster
type PVControlInterface interface {
	PatchPVReclaimPolicy(runtime.Object, *corev1.PersistentVolume, corev1.PersistentVolumeReclaimPolicy) error
	UpdateMetaInfo(runtime.Object, *corev1.PersistentVolume) (*corev1.PersistentVolume, error)
	PatchPVClaimRef(runtime.Object, *corev1.PersistentVolume, string) error
	CreatePV(obj runtime.Object, pv *corev1.PersistentVolume) error
	GetPV(name string) (*corev1.PersistentVolume, error)
}

type realPVControl struct {
	kubeCli   kubernetes.Interface
	pvcLister corelisters.PersistentVolumeClaimLister
	pvLister  corelisters.PersistentVolumeLister
	recorder  record.EventRecorder
}

// NewRealPVControl creates a new PVControlInterface
func NewRealPVControl(
	kubeCli kubernetes.Interface,
	pvcLister corelisters.PersistentVolumeClaimLister,
	pvLister corelisters.PersistentVolumeLister,
	recorder record.EventRecorder,
) PVControlInterface {
	return &realPVControl{
		kubeCli:   kubeCli,
		pvcLister: pvcLister,
		pvLister:  pvLister,
		recorder:  recorder,
	}
}

func (c *realPVControl) PatchPVReclaimPolicy(obj runtime.Object, pv *corev1.PersistentVolume, reclaimPolicy corev1.PersistentVolumeReclaimPolicy) error {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return fmt.Errorf("%+v is not a runtime.Object, cannot get controller from it", obj)
	}

	name := metaObj.GetName()
	pvName := pv.GetName()
	patchBytes := []byte(fmt.Sprintf(`{"spec":{"persistentVolumeReclaimPolicy":"%s"}}`, reclaimPolicy))

	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		_, err := c.kubeCli.CoreV1().PersistentVolumes().Patch(context.TODO(), pvName, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
		return err
	})
	c.recordPVEvent("patch", obj, name, pvName, err)
	return err
}

func (c *realPVControl) GetPV(name string) (*corev1.PersistentVolume, error) {
	return c.pvLister.Get(name)
}

func (c *realPVControl) CreatePV(obj runtime.Object, pv *corev1.PersistentVolume) error {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return fmt.Errorf("%+v is not a runtime.Object, cannot get controller from it", obj)
	}

	name := metaObj.GetName()
	pvName := pv.GetName()
	_, err := c.kubeCli.CoreV1().PersistentVolumes().Create(context.TODO(), pv, metav1.CreateOptions{})
	c.recordPVEvent("create", obj, name, pvName, err)
	return err
}

func (c *realPVControl) PatchPVClaimRef(obj runtime.Object, pv *corev1.PersistentVolume, pvcName string) error {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return fmt.Errorf("%+v is not a runtime.Object, cannot get controller from it", obj)
	}

	name := metaObj.GetName()
	pvName := pv.GetName()
	patchBytes := []byte(fmt.Sprintf(`{"spec":{"claimRef":{"name":"%s","resourceVersion":"","uid":""}}}`, pvcName))

	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		_, err := c.kubeCli.CoreV1().PersistentVolumes().Patch(context.TODO(), pvName, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
		return err
	})
	c.recordPVEvent("patch", obj, name, pvName, err)
	return err
}

func (c *realPVControl) UpdateMetaInfo(obj runtime.Object, pv *corev1.PersistentVolume) (*corev1.PersistentVolume, error) {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return nil, fmt.Errorf("%+v is not a runtime.Object, cannot get controller from it", obj)
	}

	ns := metaObj.GetNamespace()
	name := metaObj.GetName()
	kind := obj.GetObjectKind().GroupVersionKind().Kind

	if pv.Annotations == nil {
		pv.Annotations = make(map[string]string)
	}
	if pv.Labels == nil {
		pv.Labels = make(map[string]string)
	}
	pvName := pv.GetName()
	pvcRef := pv.Spec.ClaimRef
	if pvcRef == nil {
		klog.Warningf("PV: [%s] doesn't have a ClaimRef, skipping, %s: %s/%s", kind, pvName, ns, name)
		return pv, nil
	}

	pvcName := pvcRef.Name
	pvc, err := c.pvcLister.PersistentVolumeClaims(ns).Get(pvcName)
	if err != nil {
		if !apierrs.IsNotFound(err) {
			return pv, err
		}

		klog.Warningf("PV: [%s]'s PVC: [%s/%s] doesn't exist, skipping. %s: %s", pvName, ns, pvcName, kind, name)
		return pv, nil
	}

	component := pvc.Labels[label.ComponentLabelKey]
	podName := pvc.Annotations[label.AnnPodNameKey]
	clusterID := pvc.Labels[label.ClusterIDLabelKey]
	memberID := pvc.Labels[label.MemberIDLabelKey]
	storeID := pvc.Labels[label.StoreIDLabelKey]

	if pv.Labels[label.NamespaceLabelKey] == ns &&
		pv.Labels[label.ComponentLabelKey] == component &&
		pv.Labels[label.NameLabelKey] == pvc.Labels[label.NameLabelKey] &&
		pv.Labels[label.ManagedByLabelKey] == pvc.Labels[label.ManagedByLabelKey] &&
		pv.Labels[label.InstanceLabelKey] == pvc.Labels[label.InstanceLabelKey] &&
		pv.Labels[label.ClusterIDLabelKey] == clusterID &&
		pv.Labels[label.MemberIDLabelKey] == memberID &&
		pv.Labels[label.StoreIDLabelKey] == storeID &&
		pv.Annotations[label.AnnPodNameKey] == podName {
		klog.V(4).Infof("pv %s already has labels and annotations synced, skipping. %s: %s/%s", pvName, kind, ns, name)
		return pv, nil
	}

	pv.Labels[label.NamespaceLabelKey] = ns
	pv.Labels[label.ComponentLabelKey] = component
	pv.Labels[label.NameLabelKey] = pvc.Labels[label.NameLabelKey]
	pv.Labels[label.ManagedByLabelKey] = pvc.Labels[label.ManagedByLabelKey]
	pv.Labels[label.InstanceLabelKey] = pvc.Labels[label.InstanceLabelKey]

	setIfNotEmpty(pv.Labels, label.ClusterIDLabelKey, clusterID)
	setIfNotEmpty(pv.Labels, label.MemberIDLabelKey, memberID)
	setIfNotEmpty(pv.Labels, label.StoreIDLabelKey, storeID)
	setIfNotEmpty(pv.Annotations, label.AnnPodNameKey, podName)

	labels := pv.GetLabels()
	ann := pv.GetAnnotations()
	var updatePV *corev1.PersistentVolume
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		var updateErr error
		updatePV, updateErr = c.kubeCli.CoreV1().PersistentVolumes().Update(context.TODO(), pv, metav1.UpdateOptions{})
		if updateErr == nil {
			klog.Infof("PV: [%s] updated successfully, %s: %s/%s", pvName, kind, ns, name)
			return nil
		}
		klog.Errorf("failed to update PV: [%s], %s %s/%s, error: %v", pvName, kind, ns, name, err)

		if updated, err := c.pvLister.Get(pvName); err == nil {
			// make a copy so we don't mutate the shared cache
			pv = updated.DeepCopy()
			pv.Labels = labels
			pv.Annotations = ann
		} else {
			utilruntime.HandleError(fmt.Errorf("error getting updated PV %s/%s from lister: %v", ns, pvName, err))
		}
		return updateErr
	})

	return updatePV, err
}

func (c *realPVControl) recordPVEvent(verb string, obj runtime.Object, objName, pvName string, err error) {
	if err == nil {
		reason := fmt.Sprintf("Successful%s", strings.Title(verb))
		msg := fmt.Sprintf("%s PV %s in TidbCluster %s successful",
			strings.ToLower(verb), pvName, objName)
		c.recorder.Event(obj, corev1.EventTypeNormal, reason, msg)
	} else {
		reason := fmt.Sprintf("Failed%s", strings.Title(verb))
		msg := fmt.Sprintf("%s PV %s in TidbCluster %s failed error: %s",
			strings.ToLower(verb), pvName, objName, err)
		c.recorder.Event(obj, corev1.EventTypeWarning, reason, msg)
	}
}

var _ PVControlInterface = &realPVControl{}

// FakePVControl is a fake PVControlInterface
type FakePVControl struct {
	PVCLister       corelisters.PersistentVolumeClaimLister
	PVIndexer       cache.Indexer
	updatePVTracker RequestTracker
	createPVTracker RequestTracker
}

// NewFakePVControl returns a FakePVControl
func NewFakePVControl(pvInformer coreinformers.PersistentVolumeInformer, pvcInformer coreinformers.PersistentVolumeClaimInformer) *FakePVControl {
	return &FakePVControl{
		pvcInformer.Lister(),
		pvInformer.Informer().GetIndexer(),
		RequestTracker{},
		RequestTracker{},
	}
}

// SetUpdatePVError sets the error attributes of updatePVTracker
func (c *FakePVControl) SetUpdatePVError(err error, after int) {
	c.updatePVTracker.SetError(err).SetAfter(after)
}

// PatchPVReclaimPolicy patchs the reclaim policy of PV
func (c *FakePVControl) PatchPVReclaimPolicy(_ runtime.Object, pv *corev1.PersistentVolume, reclaimPolicy corev1.PersistentVolumeReclaimPolicy) error {
	defer c.updatePVTracker.Inc()
	if c.updatePVTracker.ErrorReady() {
		defer c.updatePVTracker.Reset()
		return c.updatePVTracker.GetError()
	}
	pv.Spec.PersistentVolumeReclaimPolicy = reclaimPolicy

	return c.PVIndexer.Update(pv)
}

// UpdateMetaInfo update the meta info of pv
func (c *FakePVControl) UpdateMetaInfo(obj runtime.Object, pv *corev1.PersistentVolume) (*corev1.PersistentVolume, error) {
	defer c.updatePVTracker.Inc()

	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return nil, fmt.Errorf("%+v is not a runtime.Object, cannot get controller from it", obj)
	}
	ns := metaObj.GetNamespace()
	if c.updatePVTracker.ErrorReady() {
		defer c.updatePVTracker.Reset()
		return nil, c.updatePVTracker.GetError()
	}
	pvcName := pv.Spec.ClaimRef.Name
	pvc, err := c.PVCLister.PersistentVolumeClaims(ns).Get(pvcName)
	if err != nil {
		return nil, err
	}
	if pv.Labels == nil {
		pv.Labels = make(map[string]string)
	}
	if pv.Annotations == nil {
		pv.Annotations = make(map[string]string)
	}
	pv.Labels[label.NamespaceLabelKey] = ns
	pv.Labels[label.NameLabelKey] = pvc.Labels[label.NameLabelKey]
	pv.Labels[label.ManagedByLabelKey] = pvc.Labels[label.ManagedByLabelKey]
	pv.Labels[label.InstanceLabelKey] = pvc.Labels[label.InstanceLabelKey]
	pv.Labels[label.ComponentLabelKey] = pvc.Labels[label.ComponentLabelKey]

	setIfNotEmpty(pv.Labels, label.ClusterIDLabelKey, pvc.Labels[label.ClusterIDLabelKey])
	setIfNotEmpty(pv.Labels, label.MemberIDLabelKey, pvc.Labels[label.MemberIDLabelKey])
	setIfNotEmpty(pv.Labels, label.StoreIDLabelKey, pvc.Labels[label.StoreIDLabelKey])
	setIfNotEmpty(pv.Annotations, label.AnnPodNameKey, pvc.Annotations[label.AnnPodNameKey])
	return pv, c.PVIndexer.Update(pv)
}

func (c *FakePVControl) PatchPVClaimRef(obj runtime.Object, pv *corev1.PersistentVolume, pvcName string) error {
	defer c.updatePVTracker.Inc()
	if c.updatePVTracker.ErrorReady() {
		defer c.updatePVTracker.Reset()
		return c.updatePVTracker.GetError()
	}
	pv.Spec.ClaimRef.Name = pvcName

	return c.PVIndexer.Update(pv)
}

// CreatePV create new pv
func (c *FakePVControl) CreatePV(_ runtime.Object, pv *corev1.PersistentVolume) error {
	defer c.createPVTracker.Inc()
	if c.createPVTracker.ErrorReady() {
		defer c.createPVTracker.Reset()
		return c.createPVTracker.GetError()
	}

	return c.PVIndexer.Add(pv)
}

func (c *FakePVControl) GetPV(name string) (*corev1.PersistentVolume, error) {
	defer c.updatePVTracker.Inc()
	obj, existed, err := c.PVIndexer.GetByKey(name)
	if err != nil {
		return nil, err
	}
	if !existed {
		return nil, fmt.Errorf("pvc[%s] not existed", name)
	}
	a := obj.(*corev1.PersistentVolume)
	return a, nil
}

var _ PVControlInterface = &FakePVControl{}
