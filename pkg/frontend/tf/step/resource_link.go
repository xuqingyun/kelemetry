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

package tfstep

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jaegertracing/jaeger/model"
	"k8s.io/apimachinery/pkg/runtime/schema"

	tftree "github.com/kubewharf/kelemetry/pkg/frontend/tf/tree"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

type ResourceLinkVisitor struct {
	ResourceTags []string
	Domain       string
}

func (visitor ResourceLinkVisitor) Enter(tree tftree.SpanTree, span *model.Span) tftree.TreeVisitor {
	if tagKv, hasTag := model.KeyValues(span.Tags).FindByKey(zconstants.NestLevel); !hasTag || tagKv.VStr != "object" {
		return visitor
	}
	if _, hasTag := model.KeyValues(span.Tags).FindByKey("resource"); !hasTag {
		return visitor
	}

	for _, resourceKey := range visitor.ResourceTags {
		gv := schema.ParseGroupResource(resourceKey)
		resourceKv := visitor.findTagRecursively(tree, span, resourceKey)

		if len(resourceKv.Key) > 0 {
			clusterKv, _ := model.KeyValues(span.Tags).FindByKey("cluster")
			timeStamp := span.GetStartTime().Format(time.RFC3339)

			query := url.Values{
				"cluster":  []string{clusterKv.VStr},
				"group":    []string{gv.Group},
				"resource": []string{gv.Resource},
				"name":     []string{resourceKv.VStr},
				"ts":       []string{timeStamp},
			}.Encode()
			span.Tags = append(span.Tags, model.KeyValue{
				Key:  resourceKey + "_trace",
				VStr: fmt.Sprintf("%v/redirect?%v", strings.TrimSuffix(visitor.Domain, "/"), query),
			})
		}
	}

	return visitor
}

func (visitor ResourceLinkVisitor) Exit(tree tftree.SpanTree, span *model.Span) {}

func (visitor ResourceLinkVisitor) findTagRecursively(tree tftree.SpanTree, span *model.Span, tagKey string) model.KeyValue {
	if kv, hasTag := model.KeyValues(span.Tags).FindByKey(tagKey); hasTag {
		return kv
	}

	for childId := range tree.Children(span.SpanID) {
		childSpan := tree.Span(childId)
		tagKv, _ := model.KeyValues(childSpan.Tags).FindByKey(zconstants.NestLevel)
		if tagKv.VStr == "object" {
			continue
		}
		kv := visitor.findTagRecursively(tree, childSpan, tagKey)
		if len(kv.Key) > 0 {
			span.Tags = append(span.Tags, kv)
			return kv
		}
	}
	return model.KeyValue{}
}
