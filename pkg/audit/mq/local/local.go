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

package local

import (
	"context"
	"fmt"
	"hash/fnv"
	"math/rand"
	"sync/atomic"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"

	"github.com/kubewharf/kelemetry/pkg/audit/mq"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util/channel"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

func init() {
	manager.Global.ProvideMuxImpl("mq/local", newLocal, mq.Queue.CreateProducer)
}

type localOptions struct {
	partitionByObject bool
}

func (options *localOptions) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(
		&options.partitionByObject,
		"mq-local-partition-by-object",
		false,
		"Whether to partition events of the same object to the same worker",
	)
}

func (options *localOptions) EnableFlag() *bool { return nil }

type localQueue struct {
	manager.MuxImplBase

	options localOptions
	logger  logrus.FieldLogger
	metrics metrics.Client
	ctx     context.Context

	producer      *localProducer
	consumers     map[mq.ConsumerGroup]map[mq.PartitionId]*localConsumer
	numPartitions int32
}

type lagMetric struct {
	ConsumerGroup mq.ConsumerGroup
	Partition     mq.PartitionId
}

func newLocal(
	logger logrus.FieldLogger,
	metrics metrics.Client,
) *localQueue {
	return &localQueue{
		logger:    logger,
		metrics:   metrics,
		consumers: map[mq.ConsumerGroup]map[mq.PartitionId]*localConsumer{},
	}
}

func (_ *localQueue) MuxImplName() (name string, isDefault bool) { return "local", true }

func (q *localQueue) Options() manager.Options { return &q.options }

func (q *localQueue) Init(ctx context.Context) error {
	q.ctx = ctx
	return nil
}

func (q *localQueue) Start(stopCh <-chan struct{}) error {
	numPartitions := -1
	for group, consumers := range q.consumers {
		if numPartitions != -1 && numPartitions != len(consumers) {
			return fmt.Errorf(
				"different consumer groups have different partition counts (%d != %d for %q)",
				numPartitions,
				len(consumers),
				group,
			)
		}
		numPartitions = len(consumers)
	}
	q.numPartitions = int32(numPartitions)

	for _, consumers := range q.consumers {
		for _, consumer := range consumers {
			consumer.start(stopCh)
		}
	}

	return nil
}

func (q *localQueue) Close() error {
	return nil
}

func (q *localQueue) CreateProducer() (_ mq.Producer, err error) {
	if q.producer == nil {
		q.producer = q.newLocalProducer()
	}

	return q.producer, nil
}

type localProducer struct {
	logger        logrus.FieldLogger
	queue         *localQueue
	offsetCounter int64
}

func (q *localQueue) newLocalProducer() *localProducer {
	return &localProducer{
		logger: q.logger.WithField("submod", "producer"),
		queue:  q,
	}
}

func (producer *localProducer) Send(partitionKey []byte, value []byte) error {
	var partition int32
	if producer.queue.options.partitionByObject && partitionKey != nil {
		keyHasher := fnv.New32()
		_, _ = keyHasher.Write(partitionKey) // fnv.Write is infallible
		hash := keyHasher.Sum32()
		partition = int32(hash % uint32(producer.queue.numPartitions))
	} else {
		partition = rand.Int31n(producer.queue.numPartitions)
	}

	partitionId := mq.PartitionId(partition)

	offset := atomic.AddInt64(&producer.offsetCounter, 1)

	message := localMessage{
		offset: offset,
		key:    partitionKey,
		value:  value,
	}
	for _, consumers := range producer.queue.consumers {
		consumers[partitionId].uq.Send(message)
	}

	return nil
}

func (q *localQueue) CreateConsumer(group mq.ConsumerGroup, partition mq.PartitionId, handler mq.MessageHandler) (mq.Consumer, error) {
	if _, exists := q.consumers[group]; !exists {
		q.consumers[group] = map[mq.PartitionId]*localConsumer{}
	}

	if _, exists := q.consumers[group][partition]; exists {
		return nil, fmt.Errorf("consumer for %q/%d requested multiple times", group, partition)
	}

	consumer := q.newConsumer(group, partition, handler)
	q.metrics.NewMonitor("audit_mq_local_lag", &lagMetric{
		ConsumerGroup: group,
		Partition:     partition,
	}, func() int64 { return int64(consumer.uq.Length()) })
	q.consumers[group][partition] = consumer
	return consumer, nil
}

type localConsumer struct {
	logger  logrus.FieldLogger
	handler mq.MessageHandler
	uq      *channel.UnboundedQueue[localMessage]
}

func (q *localQueue) newConsumer(group mq.ConsumerGroup, partition mq.PartitionId, handler mq.MessageHandler) *localConsumer {
	return &localConsumer{
		logger:  q.logger.WithField("submod", "consumer").WithField("group", string(group)).WithField("partition", int32(partition)),
		handler: handler,
		uq:      channel.NewUnboundedQueue[localMessage](64),
	}
}

func (consumer *localConsumer) start(stopCh <-chan struct{}) {
	go func() {
		defer shutdown.RecoverPanic(consumer.logger)

		for {
			select {
			case message, chOpen := <-consumer.uq.Receiver():
				if !chOpen {
					return
				}

				consumer.handler(consumer.logger.WithField("offset", message.offset), message.key, message.value)
			case <-stopCh:
				return
			}
		}
	}()
}

type localMessage struct {
	offset int64
	key    []byte
	value  []byte
}
