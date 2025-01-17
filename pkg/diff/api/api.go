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

package api

import (
	"context"
	"fmt"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/clock"

	diffcache "github.com/kubewharf/kelemetry/pkg/diff/cache"
	"github.com/kubewharf/kelemetry/pkg/http"
	"github.com/kubewharf/kelemetry/pkg/k8s"
	"github.com/kubewharf/kelemetry/pkg/k8s/objectcache"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

func init() {
	manager.Global.Provide("diff-api", NewApi)
}

type apiOptions struct {
	enable bool
}

func (options *apiOptions) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(&options.enable, "diff-api-enable", false, "enable diff API")
}

func (options *apiOptions) EnableFlag() *bool { return &options.enable }

type api struct {
	options     apiOptions
	logger      logrus.FieldLogger
	clock       clock.Clock
	diffCache   diffcache.Cache
	objectCache objectcache.ObjectCache
	metrics     metrics.Client
	server      http.Server
	clients     k8s.Clients

	ctx           context.Context
	requestMetric metrics.Metric
	scanMetric    metrics.Metric
}

type (
	requestMetric struct{}
	scanMetric    struct{}
)

func NewApi(
	logger logrus.FieldLogger,
	clock clock.Clock,
	diffCache diffcache.Cache,
	objectCache objectcache.ObjectCache,
	metrics metrics.Client,
	server http.Server,
	clients k8s.Clients,
) *api {
	return &api{
		logger:      logger,
		clock:       clock,
		diffCache:   diffCache,
		objectCache: objectCache,
		metrics:     metrics,
		server:      server,
		clients:     clients,
	}
}

func (api *api) Options() manager.Options {
	return &api.options
}

func (api *api) Init(ctx context.Context) error {
	api.ctx = ctx
	api.requestMetric = api.metrics.New("diff_api_request", &requestMetric{})
	api.scanMetric = api.metrics.New("diff_api_scan", &scanMetric{})

	api.server.Routes().GET("/diff/:group/:version/:resource/:namespace/:name/:rv", func(ctx *gin.Context) {
		logger := api.logger.WithField("source", ctx.Request.RemoteAddr)
		defer shutdown.RecoverPanic(logger)
		metric := &requestMetric{}
		defer api.requestMetric.DeferCount(api.clock.Now(), metric)

		if err := api.handleGet(ctx); err != nil {
			logger.WithError(err).Error()
		}
	})

	api.server.Routes().GET("/diff/:group/:version/:resource/:namespace/:name", func(ctx *gin.Context) {
		logger := api.logger.WithField("source", ctx.Request.RemoteAddr)
		defer shutdown.RecoverPanic(logger)
		metric := &scanMetric{}
		defer api.scanMetric.DeferCount(api.clock.Now(), metric)

		if err := api.handleScan(ctx); err != nil {
			logger.WithError(err).Error()
		}
	})

	return nil
}

func (api *api) Start(stopCh <-chan struct{}) error { return nil }

func (api *api) handleGet(ctx *gin.Context) error {
	group := ctx.Param("group")
	version := ctx.Param("version")
	resource := ctx.Param("resource")
	namespace := ctx.Param("namespace")
	name := ctx.Param("name")
	rv := ctx.Param("rv")

	raw, err := api.objectCache.Get(ctx, util.ObjectRef{
		GroupVersionResource: schema.GroupVersionResource{
			Group:    group,
			Version:  version,
			Resource: resource,
		},
		Namespace: namespace,
		Name:      name,
	})
	if err != nil {
		return err
	}
	if raw == nil {
		return ctx.AbortWithError(404, fmt.Errorf("object does not exist"))
	}

	object := util.ObjectRefFromUnstructured(raw, api.clients.TargetCluster().ClusterName(), schema.GroupVersionResource{
		Group:    group,
		Version:  version,
		Resource: resource,
	})

	var oldRv string
	var newRv *string
	if api.diffCache.GetCommonOptions().UseOldResourceVersion {
		oldRv = rv
	} else {
		newRv = &rv
	}

	patch, err := api.diffCache.Fetch(ctx, object, oldRv, newRv)
	if err != nil || patch == nil {
		return ctx.AbortWithError(404, fmt.Errorf("patch not found for rv: %w", err))
	}

	ctx.JSON(200, patch)

	return nil
}

func (api *api) handleScan(ctx *gin.Context) error {
	group := ctx.Param("group")
	version := ctx.Param("version")
	resource := ctx.Param("resource")
	namespace := ctx.Param("namespace")
	name := ctx.Param("name")

	limitString := ctx.Query("100")
	limit := 100
	if parsedLimit, err := strconv.Atoi(limitString); err == nil {
		limit = parsedLimit
	}

	raw, err := api.objectCache.Get(ctx, util.ObjectRef{
		GroupVersionResource: schema.GroupVersionResource{
			Group:    group,
			Version:  version,
			Resource: resource,
		},
		Namespace: namespace,
		Name:      name,
	})
	if err != nil {
		return err
	}
	if raw == nil {
		return ctx.AbortWithError(404, fmt.Errorf("object does not exist"))
	}

	object := util.ObjectRefFromUnstructured(raw, api.clients.TargetCluster().ClusterName(), schema.GroupVersionResource{
		Group:    group,
		Version:  version,
		Resource: resource,
	})
	list, err := api.diffCache.List(ctx, object, limit)
	if err != nil {
		return err
	}

	ctx.JSON(200, list)

	return nil
}

func (api *api) Close() error { return nil }
