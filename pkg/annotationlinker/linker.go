// Copyright 2023 The Kelemetry Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package annotationlinker

import (
	"context"
	"encoding/json"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kubewharf/kelemetry/pkg/aggregator/linker"
	"github.com/kubewharf/kelemetry/pkg/k8s"
	"github.com/kubewharf/kelemetry/pkg/k8s/discovery"
	"github.com/kubewharf/kelemetry/pkg/k8s/objectcache"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util"
)

func init() {
	manager.Global.Provide("annotation-linker", New)
}

type options struct {
	enable bool
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(&options.enable, "annotation-linker-enable", false, "enable annotation linker")
}

func (options *options) EnableFlag() *bool { return &options.enable }

type controller struct {
	options        options
	logger         logrus.FieldLogger
	linkers        linker.LinkerList
	clients        k8s.Clients
	discoveryCache discovery.DiscoveryCache
	objectCache    objectcache.ObjectCache
	ctx            context.Context
}

var _ manager.Component = &controller{}

func New(
	logger logrus.FieldLogger,
	linkers linker.LinkerList,
	clients k8s.Clients,
	discoveryCache discovery.DiscoveryCache,
	objectCache objectcache.ObjectCache,
) *controller {
	ctrl := &controller{
		logger:         logger,
		linkers:        linkers,
		clients:        clients,
		discoveryCache: discoveryCache,
		objectCache:    objectCache,
	}
	return ctrl
}

func (ctrl *controller) Options() manager.Options {
	return &ctrl.options
}

func (ctrl *controller) Init(ctx context.Context) error {
	ctrl.linkers.AddLinker(ctrl)
	ctrl.ctx = ctx

	return nil
}

func (ctrl *controller) Start(stopCh <-chan struct{}) error {
	return nil
}

func (ctrl *controller) Close() error {
	return nil
}

func (ctrl *controller) Lookup(ctx context.Context, object util.ObjectRef) *util.ObjectRef {
	raw := object.Raw

	logger := ctrl.logger.WithField("object", object)

	if raw == nil {
		logger.Debug("Fetching dynamic object")

		var err error
		raw, err = ctrl.objectCache.Get(ctx, object)

		if err != nil {
			logger.WithError(err).Error("cannot fetch object value")
			return nil
		}

		if raw == nil {
			logger.Debug("object no longer exists")
			return nil
		}
	}

	if ann, ok := raw.GetAnnotations()[LinkAnnotation]; ok {
		ref := &ParentLink{}
		err := json.Unmarshal([]byte(ann), ref)
		if err != nil {
			logger.WithError(err).Error("cannot parse ParentLink annotation")
			return nil
		}

		if ref.Cluster == "" {
			ref.Cluster = object.Cluster
		}

		objectRef := &util.ObjectRef{
			Cluster: ref.Cluster,
			GroupVersionResource: schema.GroupVersionResource{
				Group:    ref.GroupVersionResource.Group,
				Version:  ref.GroupVersionResource.Version,
				Resource: ref.GroupVersionResource.Resource,
			},
			Namespace: ref.Namespace,
			Name:      ref.Name,
			Uid:       ref.Uid,
		}
		logger.WithField("parent", objectRef).Debug("Resolved parent")

		return objectRef
	}

	return nil
}
