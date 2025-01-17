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

package jaegerbackend

import (
	"context"
	"encoding/json"

	"github.com/jaegertracing/jaeger/model"
	"github.com/jaegertracing/jaeger/storage/spanstore"
	"k8s.io/utils/clock"

	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
)

func init() {
	manager.Global.Provide("jaeger-backend", newBackend)
}

type Backend interface {
	// Lists the thumbnail previews of all traces.
	// This method does not return detailed spans in the traces
	// in order to reduce pressure on the backend data source
	// since they may need to be transformed anyway.
	List(ctx context.Context, query *spanstore.TraceQueryParameters) ([]*TraceThumbnail, error)
	// Gets the full tree of a trace based on the identifier returned from a prvious call to List.
	Get(ctx context.Context, identifier json.RawMessage, traceId model.TraceID) (*model.Trace, model.SpanID, error)
}

type TraceThumbnail struct {
	// Identifier is a serializable object that identifies the trace in GetTrace calls.
	Identifier any `json:"identifier"`

	// Object metadata
	Cluster  string `json:"cluster"`
	Resource string `json:"resource"`

	Spans []*model.Span

	RootSpan model.SpanID
}

type mux struct {
	*manager.Mux
	clock   clock.Clock
	metrics metrics.Client

	listMetric metrics.Metric
	getMetric  metrics.Metric
}

func newBackend(metrics metrics.Client, clock clock.Clock) Backend {
	return &mux{
		Mux:     manager.NewMux("jaeger-backend", false),
		clock:   clock,
		metrics: metrics,
	}
}

type (
	listMetric struct{}
	getMetric  struct{}
)

func (mux *mux) Init(ctx context.Context) error {
	mux.listMetric = mux.metrics.New("jaeger_backend_list", &listMetric{})
	mux.getMetric = mux.metrics.New("jaeger_backend_get", &getMetric{})

	return mux.Mux.Init(ctx)
}

func (mux *mux) List(ctx context.Context, query *spanstore.TraceQueryParameters) ([]*TraceThumbnail, error) {
	defer mux.listMetric.DeferCount(mux.clock.Now(), &listMetric{})
	return mux.Impl().(Backend).List(ctx, query)
}

func (mux *mux) Get(
	ctx context.Context,
	identifier json.RawMessage,
	traceId model.TraceID,
) (*model.Trace, model.SpanID, error) {
	defer mux.getMetric.DeferCount(mux.clock.Now(), &getMetric{})
	return mux.Impl().(Backend).Get(ctx, identifier, traceId)
}
