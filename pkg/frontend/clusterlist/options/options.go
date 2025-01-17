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

package options

import (
	"context"

	"github.com/spf13/pflag"

	"github.com/kubewharf/kelemetry/pkg/frontend/clusterlist"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.ProvideMuxImpl("jaeger-cluster-list/options", newLister, clusterlist.Lister.List)
}

type options struct {
	clusters []string
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.StringSliceVar(&options.clusters, "jaeger-cluster-names", []string{}, "cluster names allowed")
}

func (options *options) EnableFlag() *bool { return nil }

type Lister struct {
	manager.MuxImplBase
	options options
}

var _ clusterlist.Lister = &Lister{}

func newLister() *Lister {
	return &Lister{}
}

func (_ *Lister) MuxImplName() (name string, isDefault bool) { return "options", true }

func (lister *Lister) Options() manager.Options { return &lister.options }

func (lister *Lister) Init(ctx context.Context) error { return nil }

func (lister *Lister) Start(stopCh <-chan struct{}) error { return nil }

func (lister *Lister) Close() error { return nil }

func (lister *Lister) List() []string { return lister.options.clusters }
