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
	"strings"

	"github.com/jaegertracing/jaeger/model"

	tftree "github.com/kubewharf/kelemetry/pkg/frontend/tf/tree"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

type ReplaceNameVisitor struct{}

func (visitor ReplaceNameVisitor) Enter(tree tftree.SpanTree, span *model.Span) tftree.TreeVisitor {
	for _, tag := range span.Tags {
		if tag.Key == zconstants.SpanName {
			span.OperationName = tag.VStr
			break
		}
	}

	if span == tree.Root {
		span.OperationName = fmt.Sprintf(
			"%s / %s..%s",
			span.OperationName,
			span.StartTime.Format("15:04"),
			span.StartTime.Add(span.Duration).Format("15:04"),
		)
	}

	return visitor
}

func (visitor ReplaceNameVisitor) Exit(tree tftree.SpanTree, span *model.Span) {}

type PruneTagsVisitor struct{}

func (visitor PruneTagsVisitor) Enter(tree tftree.SpanTree, span *model.Span) tftree.TreeVisitor {
	span.Tags = removeZconstantKeys(span.Tags)

	for i := range span.Logs {
		log := &span.Logs[i]
		log.Fields = removeZconstantKeys(log.Fields)
	}

	return visitor
}

func (visitor PruneTagsVisitor) Exit(tree tftree.SpanTree, span *model.Span) {}

func removeZconstantKeys(tags model.KeyValues) model.KeyValues {
	newTags := model.KeyValues{}
	for _, tag := range tags {
		if !strings.HasPrefix(tag.Key, zconstants.Prefix) {
			newTags = append(newTags, tag)
		}
	}
	return newTags
}
