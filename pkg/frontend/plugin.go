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

package jaeger

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/jaegertracing/jaeger/model"
	"github.com/jaegertracing/jaeger/plugin/storage/grpc/shared"
	"github.com/jaegertracing/jaeger/storage/dependencystore"
	"github.com/jaegertracing/jaeger/storage/spanstore"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"google.golang.org/grpc"

	jaegerreader "github.com/kubewharf/kelemetry/pkg/frontend/reader"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

func init() {
	manager.Global.Provide("jaeger-storage-plugin", New)
}

type options struct {
	enable  bool
	address string
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(&options.enable, "jaeger-storage-plugin-enable", false, "enable jaeger storage plugin")
	fs.StringVar(&options.address, "jaeger-storage-plugin-address", ":17271", "storage plugin grpc server bind address")
}

func (options *options) EnableFlag() *bool { return &options.enable }

type Plugin struct {
	options    options
	logger     logrus.FieldLogger
	spanReader spanstore.Reader

	grpcServer *grpc.Server
}

var _ manager.Component = &Plugin{}

func New(
	logger logrus.FieldLogger,
	spanReader jaegerreader.Interface,
) *Plugin {
	plugin := &Plugin{
		logger:     logger,
		spanReader: spanReader,
	}
	return plugin
}

func (plugin *Plugin) Options() manager.Options {
	return &plugin.options
}

func (plugin *Plugin) Init(ctx context.Context) error {
	return nil
}

func (plugin *Plugin) Start(stopCh <-chan struct{}) error {
	sharedPlugin := shared.StorageGRPCPlugin{
		Impl: plugin,
	}
	grpcServer := grpc.NewServer()
	if err := sharedPlugin.GRPCServer(nil, grpcServer); err != nil {
		return fmt.Errorf("cannot create grpc query server: %w", err)
	}

	listener, err := net.Listen("tcp", plugin.options.address)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %w", plugin.options.address, err)
	}

	go func() {
		shutdown.RecoverPanic(plugin.logger)
		err := grpcServer.Serve(listener)
		if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			plugin.logger.WithError(err).Error()
		}
	}()

	plugin.grpcServer = grpcServer

	return nil
}

func (plugin *Plugin) Close() error {
	plugin.grpcServer.Stop()
	return nil
}

func (plugin *Plugin) DependencyReader() dependencystore.Reader { return nilDepReader{} }

func (plugin *Plugin) SpanReader() spanstore.Reader { return plugin.spanReader }

func (plugin *Plugin) SpanWriter() spanstore.Writer { return noopWriter{} }

func (plugin *Plugin) ArchiveSpanReader() spanstore.Reader { return plugin.spanReader }

func (plugin *Plugin) ArchiveSpanWriter() spanstore.Writer { return noopWriter{} }

type noopWriter struct{}

func (_ noopWriter) WriteSpan(ctx context.Context, span *model.Span) error {
	return fmt.Errorf("This is a read-only storage plugin")
}

type nilDepReader struct{}

func (_ nilDepReader) GetDependencies(ctx context.Context, endTime time.Time, lookback time.Duration) ([]model.DependencyLink, error) {
	return nil, nil
}
