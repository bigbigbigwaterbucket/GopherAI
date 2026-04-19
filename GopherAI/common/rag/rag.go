package rag

import (
	"GopherAI/common/milvus"
	"GopherAI/config"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
	embeddingArk "github.com/cloudwego/eino-ext/components/embedding/ark"
	idxmilvus "github.com/cloudwego/eino-ext/components/indexer/milvus"
	retmilvus "github.com/cloudwego/eino-ext/components/retriever/milvus"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
	"github.com/milvus-io/milvus-sdk-go/v2/entity"
)

// RAGIndexer 基于 Milvus 的向量写入（替代 Redis RediSearch）
type RAGIndexer struct {
	embedding embedding.Embedder
	indexer   *idxmilvus.Indexer
}

// RAGQuery 检索 + 同一 username/filename 过滤
type RAGQuery struct {
	retriever        retriever.Retriever
	filterExpr       string
	fallbackFilePath string // 当前 RAG 文件本地路径；向量检索失败时用全文兜底
}

// maxRAGFallbackChars 全文兜底时的最大字符数，避免超长简历撑爆上下文
const maxRAGFallbackChars = 120000

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// floatVectorSearchConverter 将查询嵌入转为 FloatVector。
// eino-ext Milvus Retriever 默认使用 BinaryVector，与本项目集合的 FieldTypeFloatVector（写入侧为 float32）不一致，
// 会导致向量检索始终 0 条，进而 RAG 静默退回「无文档」的普通对话。
func floatVectorSearchConverter() func(ctx context.Context, vectors [][]float64) ([]entity.Vector, error) {
	return func(ctx context.Context, vectors [][]float64) ([]entity.Vector, error) {
		out := make([]entity.Vector, 0, len(vectors))
		for _, v := range vectors {
			f32 := make([]float32, len(v))
			for i, x := range v {
				f32[i] = float32(x)
			}
			out = append(out, entity.FloatVector(f32))
		}
		return out, nil
	}
}

// pickLatestUserRagFile 选取用户 uploads 目录下最近修改的非目录文件（与「每用户单文件」策略一致）。
func pickLatestUserRagFile(username string) (filename string, fullPath string, err error) {
	userDir := filepath.Join("uploads", username)
	entries, err := os.ReadDir(userDir)
	if err != nil || len(entries) == 0 {
		return "", "", fmt.Errorf("no uploaded file found for user %s", username)
	}
	type fi struct {
		name    string
		path    string
		modUnix int64
	}
	var files []fi
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(userDir, e.Name())
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		files = append(files, fi{name: e.Name(), path: p, modUnix: st.ModTime().Unix()})
	}
	if len(files) == 0 {
		return "", "", fmt.Errorf("no valid file found for user %s", username)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modUnix > files[j].modUnix })
	return files[0].name, files[0].path, nil
}

func ragCollectionFields(dim int64) []*entity.Field {
	return []*entity.Field{
		entity.NewField().
			WithName("id").
			WithDescription("doc id").
			WithIsPrimaryKey(true).
			WithDataType(entity.FieldTypeVarChar).
			WithMaxLength(512),
		entity.NewField().
			WithName("vector").
			WithDescription("embedding").
			WithIsPrimaryKey(false).
			WithDataType(entity.FieldTypeFloatVector).
			WithDim(dim),
		entity.NewField().
			WithName("content").
			WithDescription("text").
			WithIsPrimaryKey(false).
			WithDataType(entity.FieldTypeVarChar).
			WithMaxLength(65535),
		entity.NewField().
			WithName("metadata").
			WithDescription("json meta").
			WithIsPrimaryKey(false).
			WithDataType(entity.FieldTypeJSON),
		entity.NewField().
			WithName("username").
			WithDescription("owner").
			WithIsPrimaryKey(false).
			WithDataType(entity.FieldTypeVarChar).
			WithMaxLength(256),
		entity.NewField().
			WithName("filename").
			WithDescription("blob file name").
			WithIsPrimaryKey(false).
			WithDataType(entity.FieldTypeVarChar).
			WithMaxLength(512),
	}
}

// gopherRow 与 ragCollectionFields 顺序、类型一致，供 InsertRows 使用
type gopherRow struct {
	ID       string    `json:"id" milvus:"name:id"`
	Content  string    `json:"content" milvus:"name:content"`
	Vector   []float32 `json:"vector" milvus:"name:vector"`
	Metadata []byte    `json:"metadata" milvus:"name:metadata"`
	Username string    `json:"username" milvus:"name:username"`
	Filename string    `json:"filename" milvus:"name:filename"`
}

func docToRows(username, filename string) func(ctx context.Context, docs []*schema.Document, vectors [][]float64) ([]interface{}, error) {
	return func(ctx context.Context, docs []*schema.Document, vectors [][]float64) ([]interface{}, error) {
		out := make([]interface{}, 0, len(docs))
		for i, doc := range docs {
			md, err := sonic.Marshal(doc.MetaData)
			if err != nil {
				return nil, err
			}
			vf := make([]float32, len(vectors[i]))
			for j, v := range vectors[i] {
				vf[j] = float32(v)
			}
			out = append(out, &gopherRow{
				ID:       doc.ID,
				Content:  doc.Content,
				Vector:   vf,
				Metadata: md,
				Username: username,
				Filename: filename,
			})
		}
		return out, nil
	}
}

// NewRAGIndexer username + 磁盘上的文件名（uuid.ext），用于 Milvus 标量过滤与删除
func NewRAGIndexer(username, filename, embeddingModel string) (*RAGIndexer, error) {
	mc := milvus.Client()
	if mc == nil {
		return nil, fmt.Errorf("milvus client is nil: configure milvusConfig.address and ensure milvus.Init in main")
	}

	ctx := context.Background()
	apiKey := firstEnv("ALI_API_KEY", "OPENAI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("embedding api key empty")
	}

	cfg := config.GetConfig()
	dim := cfg.RagModelConfig.RagDimension
	if dim <= 0 {
		return nil, fmt.Errorf("rag dimension invalid in config")
	}

	embedConfig := &embeddingArk.EmbeddingConfig{
		BaseURL: cfg.RagModelConfig.RagBaseUrl,
		APIKey:  apiKey,
		Model:   embeddingModel,
	}
	embedder, err := embeddingArk.NewEmbedder(ctx, embedConfig)
	if err != nil {
		return nil, fmt.Errorf("create embedder: %w", err)
	}

	coll := cfg.Milvus.Collection
	if coll == "" {
		coll = "gopherai_rag"
	}

	idxCfg := &idxmilvus.IndexerConfig{
		Client:              mc,
		Collection:          coll,
		Description:         "GopherAI RAG",
		Fields:              ragCollectionFields(int64(dim)),
		Embedding:           embedder,
		MetricType:          idxmilvus.COSINE,
		DocumentConverter:   docToRows(username, filename),
		EnableDynamicSchema: false,
	}

	idx, err := idxmilvus.NewIndexer(ctx, idxCfg)
	if err != nil {
		return nil, fmt.Errorf("milvus indexer: %w", err)
	}

	return &RAGIndexer{
		embedding: embedder,
		indexer:   idx,
	}, nil
}

// IndexFile 读取文件并向 Milvus 写入向量
func (r *RAGIndexer) IndexFile(ctx context.Context, filePath string) error {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	doc := &schema.Document{
		ID:      "doc_1",
		Content: string(content),
		MetaData: map[string]any{
			"source": filePath,
		},
	}

	_, err = r.indexer.Store(ctx, []*schema.Document{doc})
	if err != nil {
		return fmt.Errorf("milvus store: %w", err)
	}
	return nil
}

func escapeMilvusStr(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return s
}

// DeleteIndex 按 username + filename 删除向量（上传新文件前清理旧数据）
func DeleteIndex(ctx context.Context, username, filename string) error {
	mc := milvus.Client()
	if mc == nil {
		return nil
	}
	coll := config.GetConfig().Milvus.Collection
	if coll == "" {
		coll = "gopherai_rag"
	}
	expr := fmt.Sprintf(`username == '%s' && filename == '%s'`, escapeMilvusStr(username), escapeMilvusStr(filename))
	if err := mc.Delete(ctx, coll, "", expr); err != nil {
		return fmt.Errorf("milvus delete: %w", err)
	}
	return nil
}

// NewRAGQuery 创建检索器；检索时带上 username/filename 过滤，避免串库。
// 未连接 Milvus 时仍可根据本地已上传文件做全文 RAG（仅兜底路径，不进行向量检索）。
func NewRAGQuery(ctx context.Context, username string) (*RAGQuery, error) {
	filename, fallbackAbs, err := pickLatestUserRagFile(username)
	if err != nil {
		return nil, err
	}

	mc := milvus.Client()
	if mc == nil {
		log.Printf("[rag] milvus client nil, full-file RAG only (user=%s file=%s)", username, filename)
		return &RAGQuery{
			retriever:        nil,
			filterExpr:       "",
			fallbackFilePath: fallbackAbs,
		}, nil
	}

	cfg := config.GetConfig()
	apiKey := firstEnv("ALI_API_KEY", "OPENAI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("embedding api key empty")
	}

	embedConfig := &embeddingArk.EmbeddingConfig{
		BaseURL: cfg.RagModelConfig.RagBaseUrl,
		APIKey:  apiKey,
		Model:   cfg.RagModelConfig.RagEmbeddingModel,
	}
	embedder, err := embeddingArk.NewEmbedder(ctx, embedConfig)
	if err != nil {
		return nil, fmt.Errorf("embedder: %w", err)
	}

	coll := cfg.Milvus.Collection
	if coll == "" {
		coll = "gopherai_rag"
	}

	filterExpr := fmt.Sprintf(`username == '%s' && filename == '%s'`, escapeMilvusStr(username), escapeMilvusStr(filename))

	// eino-ext 默认 SearchParam 会 AddRadius(向量维度)+range_filter，易导致 COSINE 搜索 0 条；此处仅用 level，由 Milvus 侧默认检索行为决定召回
	searchSp, err := entity.NewIndexAUTOINDEXSearchParam(1)
	if err != nil {
		return nil, fmt.Errorf("search param: %w", err)
	}

	retCfg := &retmilvus.RetrieverConfig{
		Client:           mc,
		Collection:       coll,
		VectorField:      "vector",
		OutputFields:     []string{"id", "content", "metadata", "username", "filename"},
		MetricType:       entity.COSINE,
		TopK:             5,
		Embedding:        embedder,
		VectorConverter: floatVectorSearchConverter(),
		Sp:              searchSp,
	}

	rtr, err := retmilvus.NewRetriever(ctx, retCfg)
	if err != nil {
		log.Printf("[rag] milvus NewRetriever failed, file-only RAG: %v", err)
		return &RAGQuery{
			retriever:        nil,
			filterExpr:       "",
			fallbackFilePath: fallbackAbs,
		}, nil
	}

	return &RAGQuery{
		retriever:        rtr,
		filterExpr:       filterExpr,
		fallbackFilePath: fallbackAbs,
	}, nil
}


// RetrieveDocuments 语义检索（仅当前用户当前文件）；Milvus 无结果或报错时读取本地文件全文兜底，保证「有上传即可答」。
func (r *RAGQuery) RetrieveDocuments(ctx context.Context, query string) ([]*schema.Document, error) {
	var lastMilvusErr error
	if r.retriever != nil {
		docs, err := r.retriever.Retrieve(ctx, query, retmilvus.WithFilter(r.filterExpr))
		if err == nil && len(docs) > 0 {
			return docs, nil
		}
		lastMilvusErr = err
		if err != nil {
			log.Printf("[rag] milvus retrieve error, fallback to local file: %v", err)
		} else {
			log.Printf("[rag] milvus returned 0 docs, fallback to local file")
		}
	} else {
		log.Printf("[rag] using local full-file fallback (milvus unavailable or disabled)")
	}

	if r.fallbackFilePath == "" {
		if lastMilvusErr != nil {
			return nil, fmt.Errorf("retrieve: %w", lastMilvusErr)
		}
		return nil, fmt.Errorf("retrieve: no local file for RAG fallback")
	}
	b, rerr := os.ReadFile(r.fallbackFilePath)
	if rerr != nil {
		if lastMilvusErr != nil {
			return nil, fmt.Errorf("retrieve: %w; fallback read: %v", lastMilvusErr, rerr)
		}
		return nil, fmt.Errorf("fallback read: %w", rerr)
	}
	text := string(b)
	if len([]rune(text)) > maxRAGFallbackChars {
		rs := []rune(text)
		text = string(rs[:maxRAGFallbackChars]) + "\n\n... [内容已截断，仅展示前 " + fmt.Sprint(maxRAGFallbackChars) + " 字]"
		log.Printf("[rag] fallback text truncated to %d runes", maxRAGFallbackChars)
	}
	return []*schema.Document{{
		ID:       "local_fulltext_fallback",
		Content:  text,
		MetaData: map[string]any{"source": r.fallbackFilePath, "fallback": true},
	}}, nil
}

// BuildRAGPrompt 构建包含检索文档的提示词
func BuildRAGPrompt(query string, docs []*schema.Document) string {
	if len(docs) == 0 {
		return query
	}

	contextText := ""
	for i, doc := range docs {
		contextText += fmt.Sprintf("[文档 %d]: %s\n\n", i+1, doc.Content)
	}

	return fmt.Sprintf(`基于以下参考文档回答用户的问题。如果文档中没有相关信息，请说明无法找到相关信息。

参考文档：
%s

用户问题：%s

请提供准确、完整的回答：`, contextText, query)
}
