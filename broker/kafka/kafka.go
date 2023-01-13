package kafka

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"go.opentelemetry.io/otel/attribute"
	semConv "go.opentelemetry.io/otel/semconv/v1.12.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/go-kratos/kratos/v2/log"

	kafkaGo "github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/sasl"

	"github.com/tx7do/kratos-transport/broker"
	"github.com/tx7do/kratos-transport/tracing"
)

const (
	defaultAddr = "127.0.0.1:9092"
)

type kafkaBroker struct {
	sync.RWMutex

	readerConfig kafkaGo.ReaderConfig
	writerConfig WriterConfig

	writers map[string]*kafkaGo.Writer

	saslMechanism sasl.Mechanism

	connected    bool
	opts         broker.Options
	retriesCount int

	producerTracer *tracing.Tracer
	consumerTracer *tracing.Tracer
}

func NewBroker(opts ...broker.Option) broker.Broker {
	options := broker.NewOptionsAndApply(opts...)

	b := &kafkaBroker{
		readerConfig: kafkaGo.ReaderConfig{
			WatchPartitionChanges: true,
			MaxWait:               500 * time.Millisecond,
			Logger:                nil,
			ErrorLogger:           ErrorLogger{},
		},
		writerConfig: WriterConfig{
			Balancer:     &kafkaGo.LeastBytes{},
			Logger:       nil,
			ErrorLogger:  ErrorLogger{},
			BatchTimeout: 10 * time.Millisecond, // 内部默认为1秒，那么会造成什么情况呢？同步发送的时候，发送一次要等待1秒的时间。
			Async:        true,                  // 默认设置为异步发送，效率比较高。
		},
		writers:      make(map[string]*kafkaGo.Writer),
		opts:         options,
		retriesCount: 1,
	}

	return b
}

func (b *kafkaBroker) Name() string {
	return "kafka"
}

func (b *kafkaBroker) Address() string {
	if len(b.opts.Addrs) > 0 {
		return b.opts.Addrs[0]
	}
	return defaultAddr
}

func (b *kafkaBroker) Options() broker.Options {
	return b.opts
}

func (b *kafkaBroker) Init(opts ...broker.Option) error {
	b.opts.Apply(opts...)

	var addrs []string
	for _, addr := range b.opts.Addrs {
		if len(addr) == 0 {
			continue
		}
		addrs = append(addrs, addr)
	}
	if len(addrs) == 0 {
		addrs = []string{defaultAddr}
	}
	b.opts.Addrs = addrs
	b.readerConfig.Brokers = addrs

	if b.readerConfig.Dialer == nil {
		b.readerConfig.Dialer = kafkaGo.DefaultDialer
	}

	if value, ok := b.opts.Context.Value(queueCapacityKey{}).(int); ok {
		b.readerConfig.QueueCapacity = value
	}
	if value, ok := b.opts.Context.Value(minBytesKey{}).(int); ok {
		b.readerConfig.MinBytes = value
	}
	if value, ok := b.opts.Context.Value(maxBytesKey{}).(int); ok {
		b.readerConfig.MaxBytes = value
	}
	if value, ok := b.opts.Context.Value(maxWaitKey{}).(time.Duration); ok {
		b.readerConfig.MaxWait = value
	}
	if value, ok := b.opts.Context.Value(readLagIntervalKey{}).(time.Duration); ok {
		b.readerConfig.ReadLagInterval = value
	}
	if value, ok := b.opts.Context.Value(heartbeatIntervalKey{}).(time.Duration); ok {
		b.readerConfig.HeartbeatInterval = value
	}
	if value, ok := b.opts.Context.Value(commitIntervalKey{}).(time.Duration); ok {
		b.readerConfig.CommitInterval = value
	}
	if value, ok := b.opts.Context.Value(partitionWatchIntervalKey{}).(time.Duration); ok {
		b.readerConfig.PartitionWatchInterval = value
	}
	if value, ok := b.opts.Context.Value(watchPartitionChangesKey{}).(bool); ok {
		b.readerConfig.WatchPartitionChanges = value
	}
	if value, ok := b.opts.Context.Value(sessionTimeoutKey{}).(time.Duration); ok {
		b.readerConfig.SessionTimeout = value
	}
	if value, ok := b.opts.Context.Value(rebalanceTimeoutKey{}).(time.Duration); ok {
		b.readerConfig.RebalanceTimeout = value
	}
	if value, ok := b.opts.Context.Value(retentionTimeKey{}).(time.Duration); ok {
		b.readerConfig.RetentionTime = value
	}
	if value, ok := b.opts.Context.Value(startOffsetKey{}).(int64); ok {
		b.readerConfig.StartOffset = value
	}
	if value, ok := b.opts.Context.Value(maxAttemptsKey{}).(int); ok {
		b.readerConfig.MaxAttempts = value
	}
	if value, ok := b.opts.Context.Value(mechanismKey{}).(sasl.Mechanism); ok {
		b.saslMechanism = value

		if b.readerConfig.Dialer == nil {
			dialer := &kafkaGo.Dialer{
				Timeout:       10 * time.Second,
				DualStack:     true,
				SASLMechanism: b.saslMechanism,
			}
			b.readerConfig.Dialer = dialer
		} else {
			b.readerConfig.Dialer.SASLMechanism = b.saslMechanism
		}
	}
	if value, ok := b.opts.Context.Value(readerConfigKey{}).(kafkaGo.ReaderConfig); ok {
		b.readerConfig = value
	}
	if value, ok := b.opts.Context.Value(dialerConfigKey{}).(*kafkaGo.Dialer); ok {
		b.readerConfig.Dialer = value
	}
	if value, ok := b.opts.Context.Value(dialerTimeoutKey{}).(time.Duration); ok {
		if b.readerConfig.Dialer != nil {
			b.readerConfig.Dialer.Timeout = value
		}
	}

	if b.opts.Secure && b.opts.TLSConfig != nil {
		if b.readerConfig.Dialer == nil {
			dialer := &kafkaGo.Dialer{
				Timeout:   10 * time.Second,
				DualStack: true,
			}
			b.readerConfig.Dialer = dialer
		}
		b.readerConfig.Dialer.TLS = b.opts.TLSConfig
	}

	if cnt, ok := b.opts.Context.Value(retriesCountKey{}).(int); ok {
		b.retriesCount = cnt
	}

	if len(b.opts.Tracings) > 0 {
		b.producerTracer = tracing.NewTracer(trace.SpanKindProducer, "kafka-producer", b.opts.Tracings...)
		b.consumerTracer = tracing.NewTracer(trace.SpanKindConsumer, "kafka-consumer", b.opts.Tracings...)
	}

	if value, ok := b.opts.Context.Value(loggerKey{}).(kafkaGo.Logger); ok {
		b.readerConfig.Logger = value
		b.writerConfig.Logger = value
	}
	if value, ok := b.opts.Context.Value(errorLoggerKey{}).(kafkaGo.Logger); ok {
		b.readerConfig.ErrorLogger = value
		b.writerConfig.ErrorLogger = value
	}

	if value, ok := b.opts.Context.Value(enableLoggerKey{}).(bool); ok {
		if value {
			b.readerConfig.Logger = Logger{}
			b.writerConfig.Logger = Logger{}
		} else {
			b.readerConfig.Logger = nil
			b.writerConfig.Logger = nil
		}
	}
	if value, ok := b.opts.Context.Value(enableErrorLoggerKey{}).(bool); ok {
		if value {
			b.readerConfig.ErrorLogger = ErrorLogger{}
			b.writerConfig.ErrorLogger = ErrorLogger{}
		} else {
			b.readerConfig.ErrorLogger = nil
			b.writerConfig.ErrorLogger = nil
		}
	}

	if value, ok := b.opts.Context.Value(balancerKey{}).(string); ok {
		switch value {
		default:
		case BalancerLeastBytes:
			b.writerConfig.Balancer = &kafkaGo.LeastBytes{}
			break
		case BalancerRoundRobin:
			b.writerConfig.Balancer = &kafkaGo.RoundRobin{}
			break
		case BalancerHash:
			b.writerConfig.Balancer = &kafkaGo.Hash{}
			break
		case BalancerReferenceHash:
			b.writerConfig.Balancer = &kafkaGo.ReferenceHash{}
			break
		case BalancerCRC32Balancer:
			b.writerConfig.Balancer = &kafkaGo.CRC32Balancer{}
			break
		case BalancerMurmur2Balancer:
			b.writerConfig.Balancer = &kafkaGo.Murmur2Balancer{}
			break
		}
	}

	if value, ok := b.opts.Context.Value(batchSizeKey{}).(int); ok {
		b.writerConfig.BatchSize = value
	}

	if value, ok := b.opts.Context.Value(batchTimeoutKey{}).(time.Duration); ok {
		b.writerConfig.BatchTimeout = value
	}

	if value, ok := b.opts.Context.Value(batchBytesKey{}).(int64); ok {
		b.writerConfig.BatchBytes = value
	}

	if value, ok := b.opts.Context.Value(asyncKey{}).(bool); ok {
		b.writerConfig.Async = value
	}

	if value, ok := b.opts.Context.Value(maxAttemptsKey{}).(int); ok {
		b.writerConfig.MaxAttempts = value
	}

	if value, ok := b.opts.Context.Value(readTimeoutKey{}).(time.Duration); ok {
		b.writerConfig.ReadTimeout = value
	}

	if value, ok := b.opts.Context.Value(writeTimeoutKey{}).(time.Duration); ok {
		b.writerConfig.WriteTimeout = value
	}

	if value, ok := b.opts.Context.Value(allowAutoTopicCreationKey{}).(bool); ok {
		b.writerConfig.AllowAutoTopicCreation = value
	}

	return nil
}

func (b *kafkaBroker) Connect() error {
	b.RLock()
	if b.connected {
		b.RUnlock()
		return nil
	}
	b.RUnlock()

	kAddrs := make([]string, 0, len(b.opts.Addrs))
	for _, addr := range b.opts.Addrs {
		kAddrs = append(kAddrs, addr)
	}

	if len(kAddrs) == 0 {
		return errors.New("no available commons")
	}

	b.Lock()
	b.opts.Addrs = kAddrs
	b.readerConfig.Brokers = kAddrs
	b.connected = true
	b.Unlock()

	return nil
}

func (b *kafkaBroker) Disconnect() error {
	b.RLock()
	if !b.connected {
		b.RUnlock()
		return nil
	}
	b.RUnlock()

	b.Lock()
	defer b.Unlock()
	for _, writer := range b.writers {
		if err := writer.Close(); err != nil {
			return err
		}
	}

	b.connected = false
	return nil
}

func (b *kafkaBroker) createProducer(_ string, opts ...broker.PublishOption) *kafkaGo.Writer {
	options := broker.PublishOptions{
		Context: context.Background(),
	}
	for _, o := range opts {
		o(&options)
	}

	sharedTransport := &kafkaGo.Transport{
		SASL: b.saslMechanism,
		TLS:  b.opts.TLSConfig,
	}

	writer := &kafkaGo.Writer{
		Transport: sharedTransport,

		Addr:                   kafkaGo.TCP(b.opts.Addrs...),
		Balancer:               b.writerConfig.Balancer,
		MaxAttempts:            b.writerConfig.MaxAttempts,
		BatchSize:              b.writerConfig.BatchSize,
		BatchBytes:             b.writerConfig.BatchBytes,
		BatchTimeout:           b.writerConfig.BatchTimeout,
		ReadTimeout:            b.writerConfig.ReadTimeout,
		WriteTimeout:           b.writerConfig.WriteTimeout,
		RequiredAcks:           b.writerConfig.RequiredAcks,
		Async:                  b.writerConfig.Async,
		Logger:                 b.writerConfig.Logger,
		ErrorLogger:            b.writerConfig.ErrorLogger,
		AllowAutoTopicCreation: b.writerConfig.AllowAutoTopicCreation,
	}

	return writer
}

func (b *kafkaBroker) Publish(topic string, msg broker.Any, opts ...broker.PublishOption) error {
	buf, err := broker.Marshal(b.opts.Codec, msg)
	if err != nil {
		return err
	}

	return b.publish(topic, buf, opts...)
}

func (b *kafkaBroker) publish(topic string, buf []byte, opts ...broker.PublishOption) error {
	options := broker.PublishOptions{
		Context: context.Background(),
	}
	for _, o := range opts {
		o(&options)
	}

	kMsg := kafkaGo.Message{Topic: topic, Value: buf}

	if headers, ok := options.Context.Value(messageHeadersKey{}).(map[string]interface{}); ok {
		for k, v := range headers {
			header := kafkaGo.Header{Key: k}
			switch t := v.(type) {
			case string:
				header.Value = []byte(t)
			case []byte:
				header.Value = t
			default:
				var buf bytes.Buffer
				enc := gob.NewEncoder(&buf)
				if err := enc.Encode(v); err != nil {
					continue
				}
				header.Value = buf.Bytes()
			}
			kMsg.Headers = append(kMsg.Headers, header)
		}
	}

	if value, ok := options.Context.Value(messageKeyKey{}).([]byte); ok {
		kMsg.Key = value
	}

	if value, ok := options.Context.Value(messageOffsetKey{}).(int64); ok {
		kMsg.Offset = value
	}

	var cached bool
	b.Lock()
	writer, ok := b.writers[topic]
	if !ok {
		writer = b.createProducer(topic, opts...)
		b.writers[topic] = writer
	} else {
		cached = true
	}
	b.Unlock()

	var err error

	span := b.startProducerSpan(options.Context, &kMsg)
	defer b.finishProducerSpan(span, int32(kMsg.Partition), kMsg.Offset, err)

	err = writer.WriteMessages(options.Context, kMsg)
	if err != nil {
		log.Errorf("WriteMessages error: %s", err.Error())
		switch cached {
		case false:
			if kerr, ok := err.(kafkaGo.Error); ok {
				if kerr.Temporary() && !kerr.Timeout() {
					time.Sleep(200 * time.Millisecond)
					err = writer.WriteMessages(options.Context, kMsg)
				}
			}
		case true:
			b.Lock()
			if err = writer.Close(); err != nil {
				b.Unlock()
				break
			}
			delete(b.writers, topic)
			b.Unlock()

			writer := b.createProducer(topic, opts...)
			for i := 0; i < b.retriesCount; i++ {
				if err = writer.WriteMessages(options.Context, kMsg); err == nil {
					b.Lock()
					b.writers[topic] = writer
					b.Unlock()
					break
				}
			}
		}
	}

	return err
}

func (b *kafkaBroker) Subscribe(topic string, handler broker.Handler, binder broker.Binder, opts ...broker.SubscribeOption) (broker.Subscriber, error) {
	options := broker.SubscribeOptions{
		Context: context.Background(),
		AutoAck: true,
		Queue:   uuid.New().String(),
	}
	for _, o := range opts {
		o(&options)
	}

	readerConfig := b.readerConfig
	readerConfig.Topic = topic
	readerConfig.GroupID = options.Queue

	sub := &subscriber{
		opts:    options,
		topic:   topic,
		handler: handler,
		reader:  kafkaGo.NewReader(readerConfig),
	}

	go func() {

		for {
			select {
			case <-options.Context.Done():
				return
			default:
				msg, err := sub.reader.FetchMessage(options.Context)
				if err != nil {
					log.Errorf("FetchMessage error: %s", err.Error())
					continue
				}

				ctx, span := b.startConsumerSpan(options.Context, &msg)

				m := &broker.Message{
					Headers: kafkaHeaderToMap(msg.Headers),
					Body:    nil,
				}

				p := &publication{topic: msg.Topic, reader: sub.reader, m: m, km: msg, ctx: options.Context}

				if binder != nil {
					m.Body = binder()
				} else {
					m.Body = msg.Value
				}

				if err := broker.Unmarshal(b.opts.Codec, msg.Value, &m.Body); err != nil {
					p.err = err
					log.Errorf("[kafka]: unmarshal message failed: %v", err)
				}

				err = sub.handler(ctx, p)
				if err != nil {
					log.Errorf("[kafka]: process message failed: %v", err)
				}
				if sub.opts.AutoAck {
					if err = p.Ack(); err != nil {
						log.Errorf("[kafka]: unable to commit msg: %v", err)
					}
				}

				b.finishConsumerSpan(span)
			}
		}
	}()

	return sub, nil
}

func (b *kafkaBroker) onMessage() {

}

func (b *kafkaBroker) startProducerSpan(ctx context.Context, msg *kafkaGo.Message) trace.Span {
	if b.producerTracer == nil {
		return nil
	}

	carrier := NewMessageCarrier(msg)

	attrs := []attribute.KeyValue{
		semConv.MessagingSystemKey.String("kafka"),
		semConv.MessagingDestinationKindTopic,
		semConv.MessagingDestinationKey.String(msg.Topic),
	}

	var span trace.Span
	ctx, span = b.producerTracer.Start(ctx, carrier, attrs...)

	return span
}

func (b *kafkaBroker) finishProducerSpan(span trace.Span, partition int32, offset int64, err error) {
	if b.producerTracer == nil {
		return
	}

	attrs := []attribute.KeyValue{
		semConv.MessagingMessageIDKey.String(strconv.FormatInt(offset, 10)),
		semConv.MessagingKafkaPartitionKey.Int64(int64(partition)),
	}

	b.producerTracer.End(context.Background(), span, err, attrs...)
}

func (b *kafkaBroker) startConsumerSpan(ctx context.Context, msg *kafkaGo.Message) (context.Context, trace.Span) {
	if b.consumerTracer == nil {
		return ctx, nil
	}

	carrier := NewMessageCarrier(msg)

	attrs := []attribute.KeyValue{
		semConv.MessagingSystemKey.String("kafka"),
		semConv.MessagingDestinationKindTopic,
		semConv.MessagingDestinationKey.String(msg.Topic),
		semConv.MessagingOperationReceive,
		semConv.MessagingMessageIDKey.String(strconv.FormatInt(msg.Offset, 10)),
		semConv.MessagingKafkaPartitionKey.Int64(int64(msg.Partition)),
	}

	var span trace.Span
	ctx, span = b.consumerTracer.Start(ctx, carrier, attrs...)

	return ctx, span
}

func (b *kafkaBroker) finishConsumerSpan(span trace.Span) {
	if b.consumerTracer == nil {
		return
	}

	b.consumerTracer.End(context.Background(), span, nil)
}
