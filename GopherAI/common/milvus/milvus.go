package milvus

import (
	"context"
	"fmt"
	"log"
	"sync"

	"GopherAI/config"

	milvusclient "github.com/milvus-io/milvus-sdk-go/v2/client"
)

var (
	mu     sync.Mutex
	cli    milvusclient.Client
	inited bool
)

// Init 连接 Milvus（RAG 依赖）。address 为空则跳过，便于仅用聊天不装 Milvus。
func Init(ctx context.Context) error {
	mu.Lock()
	defer mu.Unlock()
	if inited {
		return nil
	}

	addr := config.GetConfig().Milvus.Address
	if addr == "" {
		log.Println("milvus: skipped (milvusConfig.address empty)")
		inited = true
		return nil
	}

	mc := config.GetConfig().Milvus
	c, err := milvusclient.NewClient(ctx, milvusclient.Config{
		Address:  addr,
		Username: mc.Username,
		Password: mc.Password,
	})
	if err != nil {
		return fmt.Errorf("milvus client: %w", err)
	}
	cli = c
	inited = true
	log.Printf("milvus: connected %s collection=%s", addr, mc.Collection)
	return nil
}

// Client 返回全局客户端；未初始化或未配置时为 nil。
func Client() milvusclient.Client {
	return cli
}
