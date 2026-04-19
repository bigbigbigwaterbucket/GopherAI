package kafka

import (
	"encoding/json"
	"errors"
	"log"

	"GopherAI/dao/message"
	"GopherAI/model"

	"github.com/IBM/sarama"
)

// ErrProducerNotReady 在 InitKafka 之前调用 Publish 时返回。
var ErrProducerNotReady = errors.New("kafka producer not initialized")

type MessageMQParam struct {
	SessionID string `json:"session_id"`
	Content   string `json:"content"`
	UserName  string `json:"user_name"`
	IsUser    bool   `json:"is_user"`
}

func GenerateMessageMQParam(sessionID, content, userName string, isUser bool) []byte {
	p := MessageMQParam{
		SessionID: sessionID,
		Content:   content,
		UserName:  userName,
		IsUser:    isUser,
	}
	data, _ := json.Marshal(p)
	return data
}

type messagePersistHandler struct{}

func (messagePersistHandler) Setup(_ sarama.ConsumerGroupSession) error   { return nil }
func (messagePersistHandler) Cleanup(_ sarama.ConsumerGroupSession) error { return nil }

func (messagePersistHandler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	for msg := range claim.Messages() {
		var param MessageMQParam
		if err := json.Unmarshal(msg.Value, &param); err != nil {
			log.Printf("kafka message json: %v", err)
			session.MarkMessage(msg, "")
			continue
		}
		newMsg := &model.Message{
			SessionID: param.SessionID,
			Content:   param.Content,
			UserName:  param.UserName,
			IsUser:    param.IsUser,
		}
		if _, err := message.CreateMessage(newMsg); err != nil {
			log.Printf("kafka persist message: %v", err)
			continue
		}
		session.MarkMessage(msg, "")
	}
	return nil
}
