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

package metricsnoop

import (
	"context"
	"time"

	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
)

func init() {
	manager.Global.ProvideMuxImpl("metrics/noop", newNoop, metrics.Client.New)
}

type noop struct {
	manager.MuxImplBase
}

var _ metrics.Impl = &noop{}

func newNoop() *noop {
	return &noop{}
}

func (_ *noop) MuxImplName() (name string, isDefault bool) { return "noop", true }

func (client *noop) Options() manager.Options {
	return &manager.NoOptions{}
}

func (client *noop) Init(ctx context.Context) error { return nil }

func (client *noop) Start(stopCh <-chan struct{}) error { return nil }

func (client *noop) Close() error { return nil }

func (client *noop) New(name string, tagNames []string) metrics.MetricImpl {
	return metric{}
}

type metric struct{}

func (metric metric) Count(value int64, tags []string) {}

func (metric metric) Histogram(value int64, tags []string) {}

func (metric metric) Gauge(value int64, tags []string) {}

func (metric metric) Defer(start time.Time, tags []string) {}
