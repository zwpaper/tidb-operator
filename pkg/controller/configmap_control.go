// Copyright 2019 PingCAP, Inc.
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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog"
)

// ConfigMapControlInterface manages configmaps used by TiDB clusters
type ConfigMapControlInterface interface {
	// CreateConfigMap create the given ConfigMap owned by the controller object
	CreateConfigMap(controller runtime.Object, cm *corev1.ConfigMap) (*corev1.ConfigMap, error)
	// UpdateConfigMap continuously tries to update ConfigMap to the given state owned by the controller obejct
	UpdateConfigMap(controller runtime.Object, cm *corev1.ConfigMap) (*corev1.ConfigMap, error)
	// DeleteConfigMap delete the given ConfigMap owned by the controller object
	DeleteConfigMap(controller runtime.Object, cm *corev1.ConfigMap) error
	// GetConfigMap get the ConfigMap by configMap name
	GetConfigMap(controller runtime.Object, cm *corev1.ConfigMap) (*corev1.ConfigMap, error)
}

type realConfigMapControl struct {
	kubeCli  kubernetes.Interface
	recorder record.EventRecorder
}

// NewRealSecretControl creates a new SecretControlInterface
func NewRealConfigMapControl(
	kubeCli kubernetes.Interface,
	recorder record.EventRecorder,
) ConfigMapControlInterface {
	return &realConfigMapControl{
		kubeCli:  kubeCli,
		recorder: recorder,
	}
}

func (c *realConfigMapControl) CreateConfigMap(owner runtime.Object, cm *corev1.ConfigMap) (*corev1.ConfigMap, error) {
	created, err := c.kubeCli.CoreV1().ConfigMaps(cm.Namespace).Create(context.Background(), cm, metav1.CreateOptions{})
	c.recordConfigMapEvent("create", owner, cm, err)
	return created, err
}

func (c *realConfigMapControl) UpdateConfigMap(owner runtime.Object, cm *corev1.ConfigMap) (*corev1.ConfigMap, error) {
	ns := cm.GetNamespace()
	cmName := cm.GetName()
	cmData := cm.Data

	var updatedCm *corev1.ConfigMap
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		var updateErr error
		updatedCm, updateErr = c.kubeCli.CoreV1().ConfigMaps(ns).Update(context.Background(), cm, metav1.UpdateOptions{})
		if updateErr == nil {
			klog.Infof("update ConfigMap: [%s/%s] successfully", ns, cmName)
			return nil
		}

		if updated, err := c.kubeCli.CoreV1().ConfigMaps(cm.Namespace).Get(context.Background(), cmName, metav1.GetOptions{}); err != nil {
			utilruntime.HandleError(fmt.Errorf("error getting updated ConfigMap %s/%s from lister: %v", ns, cmName, err))
		} else {
			cm = updated.DeepCopy()
			cm.Data = cmData
		}

		return updateErr
	})
	return updatedCm, err
}

func (c *realConfigMapControl) DeleteConfigMap(owner runtime.Object, cm *corev1.ConfigMap) error {
	err := c.kubeCli.CoreV1().ConfigMaps(cm.Namespace).Delete(context.TODO(), cm.Name, metav1.DeleteOptions{})
	c.recordConfigMapEvent("delete", owner, cm, err)
	return err
}

func (c *realConfigMapControl) GetConfigMap(owner runtime.Object, cm *corev1.ConfigMap) (*corev1.ConfigMap, error) {
	existConfigMap, err := c.kubeCli.CoreV1().ConfigMaps(cm.Namespace).Get(context.TODO(), cm.Name, metav1.GetOptions{})
	return existConfigMap, err
}

func (c *realConfigMapControl) recordConfigMapEvent(verb string, owner runtime.Object, cm *corev1.ConfigMap, err error) {
	kind := owner.GetObjectKind().GroupVersionKind().Kind
	var name string
	if accessor, ok := owner.(metav1.ObjectMetaAccessor); ok {
		name = accessor.GetObjectMeta().GetName()
	}
	cmName := cm.GetName()
	if err == nil {
		reason := fmt.Sprintf("Successful%s", strings.Title(verb))
		msg := fmt.Sprintf("%s ConfigMap %s for %s/%s successful",
			strings.ToLower(verb), cmName, kind, name)
		c.recorder.Event(owner, corev1.EventTypeNormal, reason, msg)
	} else {
		reason := fmt.Sprintf("Failed%s", strings.Title(verb))
		msg := fmt.Sprintf("%s ConfigMap %s for %s/%s failed error: %s",
			strings.ToLower(verb), cmName, kind, name, err)
		c.recorder.Event(owner, corev1.EventTypeWarning, reason, msg)
	}
}

var _ ConfigMapControlInterface = &realConfigMapControl{}

// NewFakeConfigMapControl returns a FakeConfigMapControl
func NewFakeConfigMapControl(cmInformer coreinformers.ConfigMapInformer) *FakeConfigMapControl {
	return &FakeConfigMapControl{
		cmInformer.Informer().GetIndexer(),
		RequestTracker{},
		RequestTracker{},
		RequestTracker{},
		RequestTracker{},
	}
}

// FakeConfigMapControl is a fake ConfigMapControlInterface
type FakeConfigMapControl struct {
	CmIndexer              cache.Indexer
	createConfigMapTracker RequestTracker
	updateConfigMapTracker RequestTracker
	deleteConfigMapTracker RequestTracker
	getConfigMapTracker    RequestTracker
}

// SetCreateConfigMapError sets the error attributes of createConfigMapTracker
func (c *FakeConfigMapControl) SetCreateConfigMapError(err error, after int) {
	c.createConfigMapTracker.SetError(err).SetAfter(after)
}

// SetUpdateConfigMapError sets the error attributes of updateConfigMapTracker
func (c *FakeConfigMapControl) SetUpdateConfigMapError(err error, after int) {
	c.updateConfigMapTracker.SetError(err).SetAfter(after)
}

// SetDeleteConfigMapError sets the error attributes of deleteConfigMapTracker
func (c *FakeConfigMapControl) SetDeleteConfigMapError(err error, after int) {
	c.deleteConfigMapTracker.SetError(err).SetAfter(after)
}

// CreateConfigMap adds the ConfigMap to ConfigMapIndexer
func (c *FakeConfigMapControl) CreateConfigMap(_ runtime.Object, cm *corev1.ConfigMap) (*corev1.ConfigMap, error) {
	defer c.createConfigMapTracker.Inc()
	if c.createConfigMapTracker.ErrorReady() {
		defer c.createConfigMapTracker.Reset()
		return nil, c.createConfigMapTracker.GetError()
	}

	err := c.CmIndexer.Add(cm)
	if err != nil {
		return nil, err
	}
	return cm, nil
}

// UpdateConfigMap updates the ConfigMap of CmIndexer
func (c *FakeConfigMapControl) UpdateConfigMap(_ runtime.Object, cm *corev1.ConfigMap) (*corev1.ConfigMap, error) {
	defer c.updateConfigMapTracker.Inc()
	if c.updateConfigMapTracker.ErrorReady() {
		defer c.updateConfigMapTracker.Reset()
		return nil, c.updateConfigMapTracker.GetError()
	}

	return cm, c.CmIndexer.Update(cm)
}

// DeleteConfigMap deletes the ConfigMap of CmIndexer
func (c *FakeConfigMapControl) DeleteConfigMap(_ runtime.Object, _ *corev1.ConfigMap) error {
	return nil
}

func (c *FakeConfigMapControl) GetConfigMap(controller runtime.Object, cm *corev1.ConfigMap) (*corev1.ConfigMap, error) {
	defer c.getConfigMapTracker.Inc()
	if c.getConfigMapTracker.ErrorReady() {
		defer c.getConfigMapTracker.Reset()
		return nil, c.getConfigMapTracker.GetError()
	}
	return cm, nil
}

var _ ConfigMapControlInterface = &FakeConfigMapControl{}
