package kafka

import (
	"context"
	"log"
	"sync"

	"GopherAI/config"

	"github.com/IBM/sarama"
)

var (
	initOnce      sync.Once
	producer      sarama.SyncProducer
	consumerGroup sarama.ConsumerGroup
	topic         string
)

// Publish 将聊天消息异步投递到 Kafka（消费者负责写入 MySQL）。
func Publish(payload []byte) error {
	if producer == nil {
		return ErrProducerNotReady
	}
	msg := &sarama.ProducerMessage{
		Topic: topic,
		Value: sarama.ByteEncoder(payload),
	}
	_, _, err := producer.SendMessage(msg)
	return err
}

// InitKafka 初始化同步生产者，并启动消费者组（异步落库）。
func InitKafka() {
	initOnce.Do(func() {
		cfg := config.GetConfig()
		topic = cfg.Kafka.Topic
		if topic == "" {
			log.Fatal("kafkaConfig.topic is empty")
		}
		brokers := cfg.Kafka.Brokers
		if len(brokers) == 0 {
			log.Fatal("kafkaConfig.brokers is empty")
		}
		group := cfg.Kafka.ConsumerGroup
		if group == "" {
			log.Fatal("kafkaConfig.consumerGroup is empty")
		}

		saramaCfg := newSaramaConfig()

		p, err := sarama.NewSyncProducer(brokers, saramaCfg)
		if err != nil {
			log.Fatalf("kafka sync producer: %v", err)
		}
		producer = p

		consumerGroup, err = sarama.NewConsumerGroup(brokers, group, saramaCfg)
		if err != nil {
			log.Fatalf("kafka consumer group: %v", err)
		}

		go func() {
			for e := range consumerGroup.Errors() {
				log.Printf("kafka consumer error: %v", e)
			}
		}()

		handler := &messagePersistHandler{}
		ctx := context.Background()
		go func() {
			for {
				err := consumerGroup.Consume(ctx, []string{topic}, handler)
				if err != nil {
					log.Printf("kafka consume session: %v", err)
				}
				if ctx.Err() != nil {
					return
				}
			}
		}()
		log.Printf("kafka init ok brokers=%v topic=%s group=%s", brokers, topic, group)
	})
}

func newSaramaConfig() *sarama.Config {
	c := sarama.NewConfig()
	c.Version = sarama.V2_8_2_0
	c.Consumer.Return.Errors = true
	c.Consumer.Group.Rebalance.GroupStrategies = []sarama.BalanceStrategy{
		sarama.NewBalanceStrategyRoundRobin(),
	}
	c.Consumer.Offsets.Initial = sarama.OffsetOldest

	c.Producer.RequiredAcks = sarama.WaitForLocal
	c.Producer.Retry.Max = 3
	c.Producer.Return.Successes = true

	return c
}
