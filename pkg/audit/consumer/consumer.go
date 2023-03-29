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

package auditconsumer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	auditv1 "k8s.io/apiserver/pkg/apis/audit/v1"
	"k8s.io/utils/clock"

	"github.com/kubewharf/kelemetry/pkg/aggregator"
	"github.com/kubewharf/kelemetry/pkg/aggregator/event"
	"github.com/kubewharf/kelemetry/pkg/audit"
	"github.com/kubewharf/kelemetry/pkg/audit/mq"
	"github.com/kubewharf/kelemetry/pkg/filter"
	"github.com/kubewharf/kelemetry/pkg/k8s/discovery"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

func init() {
	manager.Global.Provide("audit-consumer", New)
}

type options struct {
	enable            bool
	consumerGroup     string
	partitions        []int32
	clusterFilter     string
	ignoreImpersonate bool
	enableSubObject   bool
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(&options.enable, "audit-consumer-enable", false, "enable audit consumer")
	fs.StringVar(&options.consumerGroup, "audit-consumer-group", "kelemetry", "audit consumer group name")
	fs.Int32SliceVar(&options.partitions, "audit-consumer-partition", []int32{0, 1, 2, 3, 4}, "audit message queue partitions to consume")
	fs.StringVar(
		&options.clusterFilter,
		"audit-consumer-filter-cluster-name",
		"",
		"if nonempty, only audit events with this cluster name are processed",
	)
	fs.BoolVar(
		&options.ignoreImpersonate,
		"audit-consumer-ignore-impersonate-username",
		false,
		"if set to true, direct username is always used even with impersonation",
	)
	fs.BoolVar(&options.enableSubObject, "audit-consumer-group-failures", false, "whether failed requests should be grouped together")
}

func (options *options) EnableFlag() *bool { return &options.enable }

type receiver struct {
	options        options
	logger         logrus.FieldLogger
	clock          clock.Clock
	aggregator     aggregator.Aggregator
	mq             mq.Queue
	decoratorList  audit.DecoratorList
	filter         filter.Filter
	metrics        metrics.Client
	discoveryCache discovery.DiscoveryCache

	ctx              context.Context
	consumeMetric    metrics.Metric
	e2eLatencyMetric metrics.Metric
	consumers        map[mq.PartitionId]mq.Consumer
}

var _ manager.Component = &receiver{}

type consumeMetric struct {
	Cluster       string
	ConsumerGroup mq.ConsumerGroup
	Partition     mq.PartitionId
	HasTrace      bool
}

type e2eLatencyMetric struct {
	Cluster  string
	ApiGroup schema.GroupVersion
	Resource string
}

func New(
	logger logrus.FieldLogger,
	clock clock.Clock,
	aggregator aggregator.Aggregator,
	queue mq.Queue,
	decoratorList audit.DecoratorList,
	filter filter.Filter,
	metrics metrics.Client,
	discoveryCache discovery.DiscoveryCache,
) *receiver {
	return &receiver{
		logger:         logger,
		clock:          clock,
		aggregator:     aggregator,
		mq:             queue,
		decoratorList:  decoratorList,
		filter:         filter,
		metrics:        metrics,
		discoveryCache: discoveryCache,
		consumers:      map[mq.PartitionId]mq.Consumer{},
	}
}

func (recv *receiver) Options() manager.Options {
	return &recv.options
}

func (recv *receiver) Init(ctx context.Context) error {
	recv.ctx = ctx
	recv.consumeMetric = recv.metrics.New("audit_consumer_event", &consumeMetric{})
	recv.e2eLatencyMetric = recv.metrics.New("audit_consumer_e2e_latency", &e2eLatencyMetric{})

	for _, partition := range recv.options.partitions {
		group := mq.ConsumerGroup(recv.options.consumerGroup)
		partition := mq.PartitionId(partition)
		consumer, err := recv.mq.CreateConsumer(
			group,
			partition,
			func(fieldLogger logrus.FieldLogger, msgKey []byte, msgValue []byte) {
				recv.handleMessage(fieldLogger, msgKey, msgValue, group, partition)
			},
		)
		if err != nil {
			return fmt.Errorf("cannot create consumer for partition %d: %w", partition, err)
		}
		recv.consumers[partition] = consumer
	}

	return nil
}

func (recv *receiver) Start(stopCh <-chan struct{}) error {
	return nil
}

func (recv *receiver) Close() error {
	recv.logger.Info("receiver close")
	return nil
}

func (recv *receiver) handleMessage(
	fieldLogger logrus.FieldLogger,
	msgKey []byte,
	msgValue []byte,
	consumerGroup mq.ConsumerGroup,
	partition mq.PartitionId,
) {
	logger := fieldLogger.WithField("mod", "audit-consumer")

	metric := &consumeMetric{
		ConsumerGroup: consumerGroup,
		Partition:     partition,
	}
	defer recv.consumeMetric.DeferCount(recv.clock.Now(), metric)

	// The first part of the message key is always the cluster no matter what partitioning method we use.
	cluster := strings.SplitN(string(msgKey), "/", 2)[0]
	metric.Cluster = cluster

	if recv.options.clusterFilter != "" && recv.options.clusterFilter != cluster {
		return
	}

	message := &audit.Message{}
	if err := json.Unmarshal(msgValue, message); err != nil {
		logger.WithError(err).Error("error decoding audit data")
		return
	}

	recv.handleItem(logger.WithField("auditId", message.Event.AuditID), message, metric)
}

var supportedVerbs = sets.NewString(
	audit.VerbCreate,
	audit.VerbUpdate,
	audit.VerbDelete,
	audit.VerbPatch,
)

func (recv *receiver) handleItem(
	logger logrus.FieldLogger,
	message *audit.Message,
	metric *consumeMetric,
) {
	defer shutdown.RecoverPanic(logger)

	if !supportedVerbs.Has(message.Verb) {
		return
	}

	if message.Stage != auditv1.StageResponseComplete {
		return
	}

	if !recv.filter.TestAuditEvent(&message.Event) {
		return
	}

	if message.ObjectRef == nil || message.ObjectRef.Name == "" {
		// try reconstructing ObjectRef from ResponseObject
		if err := recv.inferObjectRef(message); err != nil {
			logger.WithError(err).Debug("Invalid objectRef, cannot infer")
			return
		}
	}

	objectRef := util.ObjectRef{
		Cluster: message.Cluster,
		GroupVersionResource: schema.GroupVersionResource{
			Group:    message.ObjectRef.APIGroup,
			Version:  message.ObjectRef.APIVersion,
			Resource: message.ObjectRef.Resource,
		},
		Namespace: message.ObjectRef.Namespace,
		Name:      message.ObjectRef.Name,
		Uid:       message.ObjectRef.UID,
	}

	if message.ResponseObject != nil {
		objectRef.Raw = &unstructured.Unstructured{
			Object: map[string]any{},
		}

		err := json.Unmarshal(message.ResponseObject.Raw, &objectRef.Raw.Object)
		if err != nil {
			logger.Errorf("cannot decode responseObject: %s", err.Error())
			return
		}
	}

	field := "spec"
	if message.Verb == audit.VerbUpdate && message.ObjectRef.Subresource == "status" {
		field = "status"
	} else if message.Verb == audit.VerbDelete {
		field = "deletion"
	}

	e2eLatency := recv.clock.Since(message.StageTimestamp.Time)

	fieldLogger := logger.
		WithField("verb", message.Verb).
		WithField("field", field).
		WithField("cluster", message.Cluster).
		WithField("resource", message.ObjectRef.Resource).
		WithField("namespace", message.ObjectRef.Namespace).
		WithField("name", message.ObjectRef.Name).
		WithField("latency", e2eLatency)

	username := message.User.Username
	if message.ImpersonatedUser != nil && !recv.options.ignoreImpersonate {
		username = message.ImpersonatedUser.Username
	}
	title := fmt.Sprintf("%s %s", username, message.Verb)
	if message.ObjectRef.Subresource != "" {
		title += fmt.Sprintf(" %s", message.ObjectRef.Subresource)
	}
	if message.ResponseStatus.Code >= 300 {
		title += fmt.Sprintf(" (%s)", http.StatusText(int(message.ResponseStatus.Code)))
	}

	event := event.NewEvent(field, title, message.RequestReceivedTimestamp.Time, "audit").
		WithEndTime(message.StageTimestamp.Time).
		WithTag("username", username).
		WithTag("userAgent", message.UserAgent).
		WithTag("responseCode", message.ResponseStatus.Code).
		WithTag("resourceVersion", message.ObjectRef.ResourceVersion).
		WithTag("apiserver", message.SourceAddr).
		WithTag("tag", message.Verb)

	recv.decoratorList.Decorate(message, event)

	recv.e2eLatencyMetric.With(&e2eLatencyMetric{
		Cluster:  message.Cluster,
		ApiGroup: objectRef.GroupVersion(),
		Resource: objectRef.Resource,
	}).Histogram(e2eLatency.Nanoseconds())

	ctx, cancelFunc := context.WithCancel(recv.ctx)
	defer cancelFunc()

	var subObjectId *aggregator.SubObjectId
	if recv.options.enableSubObject && (message.Verb == audit.VerbUpdate || message.Verb == audit.VerbPatch) {
		subObjectId = &aggregator.SubObjectId{
			Id:      fmt.Sprintf("rv=%s", message.ObjectRef.ResourceVersion),
			Primary: message.ResponseStatus.Code < 300,
		}
	}

	err := recv.aggregator.Send(ctx, objectRef, event, subObjectId)
	if err != nil {
		fieldLogger.WithError(err).Error()
	} else {
		fieldLogger.Debug("Send")
	}

	metric.HasTrace = true
}

func (receiver *receiver) inferObjectRef(message *audit.Message) error {
	if message.ResponseObject != nil {
		var partial metav1.PartialObjectMetadata
		if err := json.Unmarshal(message.ResponseObject.Raw, &partial); err != nil {
			return fmt.Errorf("unmarshal raw object: %w", err)
		}

		gv, err := schema.ParseGroupVersion(partial.APIVersion)
		if err != nil {
			return fmt.Errorf("object has invalid GroupVersion: %w", err)
		}

		cdc, err := receiver.discoveryCache.ForCluster(message.Cluster)
		if err != nil {
			return fmt.Errorf("no ClusterDiscoveryCache: %w", err)
		}

		gvr, ok := cdc.LookupResource(gv.WithKind(partial.Kind))
		if !ok {
			return fmt.Errorf("conversion of response to GVR failed")
		}

		message.ObjectRef = &auditv1.ObjectReference{
			APIGroup:   gvr.Group,
			APIVersion: gvr.Version,
			Resource:   gvr.Resource,
			Namespace:  partial.Namespace,
			Name:       partial.Name,
			UID:        partial.UID,
			// do not set as partial.ResourceVersion, otherwise diff decorator will think this is a no-op
			ResourceVersion: "",
			// TODO try to infer subresource from RequestObject
			Subresource: "",
		}

		return nil
	}

	return fmt.Errorf("no ResponseObject to infer objectRef from")
}
