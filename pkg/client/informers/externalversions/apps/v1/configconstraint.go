/*
Copyright (C) 2022-2024 ApeCloud Co., Ltd

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

// Code generated by informer-gen. DO NOT EDIT.

package v1

import (
	"context"
	time "time"

	appsv1 "github.com/apecloud/kubeblocks/apis/apps/v1"
	versioned "github.com/apecloud/kubeblocks/pkg/client/clientset/versioned"
	internalinterfaces "github.com/apecloud/kubeblocks/pkg/client/informers/externalversions/internalinterfaces"
	v1 "github.com/apecloud/kubeblocks/pkg/client/listers/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	watch "k8s.io/apimachinery/pkg/watch"
	cache "k8s.io/client-go/tools/cache"
)

// ConfigConstraintInformer provides access to a shared informer and lister for
// ConfigConstraints.
type ConfigConstraintInformer interface {
	Informer() cache.SharedIndexInformer
	Lister() v1.ConfigConstraintLister
}

type configConstraintInformer struct {
	factory          internalinterfaces.SharedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
}

// NewConfigConstraintInformer constructs a new informer for ConfigConstraint type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewConfigConstraintInformer(client versioned.Interface, resyncPeriod time.Duration, indexers cache.Indexers) cache.SharedIndexInformer {
	return NewFilteredConfigConstraintInformer(client, resyncPeriod, indexers, nil)
}

// NewFilteredConfigConstraintInformer constructs a new informer for ConfigConstraint type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFilteredConfigConstraintInformer(client versioned.Interface, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) cache.SharedIndexInformer {
	return cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.AppsV1().ConfigConstraints().List(context.TODO(), options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.AppsV1().ConfigConstraints().Watch(context.TODO(), options)
			},
		},
		&appsv1.ConfigConstraint{},
		resyncPeriod,
		indexers,
	)
}

func (f *configConstraintInformer) defaultInformer(client versioned.Interface, resyncPeriod time.Duration) cache.SharedIndexInformer {
	return NewFilteredConfigConstraintInformer(client, resyncPeriod, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}, f.tweakListOptions)
}

func (f *configConstraintInformer) Informer() cache.SharedIndexInformer {
	return f.factory.InformerFor(&appsv1.ConfigConstraint{}, f.defaultInformer)
}

func (f *configConstraintInformer) Lister() v1.ConfigConstraintLister {
	return v1.NewConfigConstraintLister(f.Informer().GetIndexer())
}
