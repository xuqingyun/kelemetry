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

package jaegerhttp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jaegertracing/jaeger/model/json"
	"github.com/jaegertracing/jaeger/storage/spanstore"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"k8s.io/utils/clock"

	"github.com/kubewharf/kelemetry/pkg/frontend/clusterlist"
	jaegerreader "github.com/kubewharf/kelemetry/pkg/frontend/reader"
	tfconfig "github.com/kubewharf/kelemetry/pkg/frontend/tf/config"
	pkghttp "github.com/kubewharf/kelemetry/pkg/http"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

func init() {
	manager.Global.Provide("jaeger-redirect-server", NewRedirectServer)
}

type options struct {
	enable bool
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(&options.enable, "jaeger-redirect-server-enable", false, "enable redirect server for frontend")
}

func (options *options) EnableFlag() *bool { return &options.enable }

type server struct {
	options          options
	logger           logrus.FieldLogger
	clock            clock.Clock
	server           pkghttp.Server
	metrics          metrics.Client
	spanReader       jaegerreader.Interface
	clusterList      clusterlist.Lister
	transformConfigs tfconfig.Provider

	ctx           context.Context
	requestMetric metrics.Metric
}

type requestMetric struct {
	Error metrics.LabeledError
}

func NewRedirectServer(
	logger logrus.FieldLogger,
	clock clock.Clock,
	httpServer pkghttp.Server,
	metrics metrics.Client,
	spanReader jaegerreader.Interface,
	clusterList clusterlist.Lister,
	transformConfigs tfconfig.Provider,
) *server {
	return &server{
		logger:           logger,
		clock:            clock,
		server:           httpServer,
		metrics:          metrics,
		spanReader:       spanReader,
		clusterList:      clusterList,
		transformConfigs: transformConfigs,
	}
}

func (server *server) Options() manager.Options {
	return &server.options
}

func (server *server) Init(ctx context.Context) error {
	server.ctx = ctx
	server.requestMetric = server.metrics.New("redirect_request", &requestMetric{})

	server.server.Routes().GET("/redirect", func(ctx *gin.Context) {
		logger := server.logger.WithField("source", ctx.Request.RemoteAddr)
		defer shutdown.RecoverPanic(logger)
		metric := &requestMetric{}
		defer server.requestMetric.DeferCount(server.clock.Now(), metric)

		logger.WithField("query", ctx.Request.URL.RawQuery).Infof("GET /redirect %v", ctx.Request.URL.Query())

		if code, err := server.handleGet(ctx, metric); err != nil {
			logger.WithError(err).Error()
			ctx.Status(code)
			_, _ = ctx.Writer.WriteString(err.Error())
			ctx.Abort()
		}
	})

	return nil
}

func (server *server) Start(stopCh <-chan struct{}) error { return nil }

func (server *server) handleGet(ctx *gin.Context, metric *requestMetric) (code int, err error) {
	cluster := ctx.Query("cluster")
	resource := ctx.Query("resource")
	namespace := ctx.Query("namespace")
	name := ctx.Query("name")

	if len(cluster) == 0 || len(resource) == 0 || len(name) == 0 {
		metric.Error = metrics.MakeLabeledError("EmptyParam")
		return 400, fmt.Errorf("cluster or resource or name is empty")
	}

	var hasCluster bool
	for _, knownCluster := range server.clusterList.List() {
		if strings.EqualFold(strings.ToLower(knownCluster), strings.ToLower(cluster)) {
			hasCluster = true
		}
	}
	if !hasCluster {
		metric.Error = metrics.MakeLabeledError("UnknownCluster")
		return 404, fmt.Errorf("cluster %s not supported now", cluster)
	}

	timestamp, err := time.Parse(time.RFC3339, ctx.Query("ts"))
	if err != nil {
		metric.Error = metrics.MakeLabeledError("InvalidTimestamp")
		return 400, fmt.Errorf("invalid timestamp for ts param %w", err)
	}

	tags := map[string]string{
		"resource": resource,
		"name":     name,
	}
	if namespace != "" {
		tags["namespace"] = namespace
	}

	parameters := &spanstore.TraceQueryParameters{
		ServiceName:   server.transformConfigs.DefaultName(),
		OperationName: cluster,
		Tags:          tags,
		StartTimeMin:  timestamp.Truncate(time.Minute * 30),
		StartTimeMax:  timestamp.Truncate(time.Minute * 30).Add(time.Minute * 30),
	}
	traceIDs, err := server.spanReader.FindTraceIDs(context.Background(), parameters)
	if err != nil {
		metric.Error = metrics.MakeLabeledError("TraceError")
		return 500, fmt.Errorf("failed to find trace ids %w", err)
	}

	if len(traceIDs) > 1 {
		metric.Error = metrics.MakeLabeledError("MultiTraceMatch")
		return 500, fmt.Errorf("trace ids match query length is %d, not 1", len(traceIDs))
	}
	if len(traceIDs) == 0 {
		metric.Error = metrics.MakeLabeledError("NoTraceMatch")
		emptyTrace := json.Trace{
			Spans: []json.Span{
				{
					StartTime: uint64(timestamp.UnixNano() / 1000),
					Duration:  uint64((time.Minute * 30).Microseconds()),
					Tags: []json.KeyValue{
						{Key: "cluster", Value: cluster},
						{Key: "resource", Value: resource},
						{Key: "namespace", Value: namespace},
						{Key: "name", Value: name},
					},
					Warnings: []string{"no events found"},
				},
			},
		}

		ctx.JSON(200, emptyTrace)
		return 0, nil
	}

	ctx.Redirect(302, fmt.Sprintf("/trace/%s", traceIDs[0].String()))
	return 0, nil
}

func (server *server) Close() error { return nil }
