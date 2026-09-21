// Package kb 知识库:入库(Ingest)、混合检索(Search)、级联删除(§8.5)。
// 实现 tools.KBSearcher 接口,供内置工具 search_kb / get_document 使用。
package kb

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"unicode"

	"agent-platform/internal/embedding"
	"agent-platform/internal/model"

	"gorm.io/gorm"
)

// Service 知识库服务。
type Service struct {
	db            *gorm.DB
	embed         *embedding.Client
	topK          int // 最终返回条数
	chunkMaxChars int
	overlapChars  int

	mu   sync.RWMutex
	vecs map[uint64][]float32 // chunkID -> 归一化向量(启动 Load 全量加载,Ingest 增量更新)

	jobs chan ingestJob // 异步入库队列
}

// ingestJob 异步入库任务(内容随任务传递,文档表不存原文)。
type ingestJob struct {
	docID   uint64
	content string
}

// NewService 构造知识库服务。
func NewService(db *gorm.DB, embedClient *embedding.Client, topK, chunkMaxChars, overlapChars int) *Service {
	return &Service{
		db:            db,
		embed:         embedClient,
		topK:          topK,
		chunkMaxChars: chunkMaxChars,
		overlapChars:  overlapChars,
		vecs:          map[uint64][]float32{},
		jobs:          make(chan ingestJob, 32),
	}
}

// Start 启动异步入库 worker(§8.5 入库异步)。
func (s *Service) Start(ctx context.Context, workers int) {
	if workers <= 0 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		go s.worker(ctx)
	}
	slog.Info("知识库异步入库 worker 已启动", "workers", workers)
}

// worker 消费入库任务。
func (s *Service) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-s.jobs:
			if err := s.Ingest(ctx, job.docID, job.content); err != nil {
				slog.Error("文档入库失败", "doc_id", job.docID, "err", err)
				s.db.Model(&model.KbDocument{}).Where("id=?", job.docID).Update("status", "failed")
				continue
			}
			slog.Info("文档入库完成", "doc_id", job.docID)
		}
	}
}

// Submit 提交入库任务(异步处理)。
func (s *Service) Submit(docID uint64, content string) {
	s.jobs <- ingestJob{docID: docID, content: content}
}

// Load 启动时把全量向量载入内存(§8.5 内存余弦)。
func (s *Service) Load(ctx context.Context) error {
	var rows []struct {
		ID        uint64
		Embedding []byte
	}
	if err := s.db.WithContext(ctx).Model(&model.KbChunk{}).
		Select("id", "embedding").Scan(&rows).Error; err != nil {
		return fmt.Errorf("加载向量缓存: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vecs = make(map[uint64][]float32, len(rows))
	for _, r := range rows {
		s.vecs[r.ID] = decodeVector(r.Embedding)
	}
	slog.Info("知识库向量缓存加载完成", "chunks", len(rows))
	return nil
}

// Ingest 入库:清洗 -> 切块 -> 逐批 embedding -> 事务写 kb_chunks -> 更新文档状态。
func (s *Service) Ingest(ctx context.Context, docID uint64, content string) error {
	chunks := chunkText(clean(content), s.chunkMaxChars, s.overlapChars)
	if len(chunks) == 0 {
		slog.Warn("文档切块为空", "doc_id", docID)
		return s.db.Model(&model.KbDocument{}).Where("id=?", docID).
			Updates(map[string]any{"status": "ready", "chunk_count": 0}).Error
	}

	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.text
	}
	vecs, err := s.embed.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("向量化失败(文档 %d): %w", docID, err)
	}

	var created []model.KbChunk
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for i, c := range chunks {
			row := model.KbChunk{
				DocID:     docID,
				Seq:       c.seq,
				Content:   c.text,
				Embedding: encodeVector(vecs[i]),
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			created = append(created, row)
		}
		return tx.Model(&model.KbDocument{}).Where("id=?", docID).
			Updates(map[string]any{"status": "ready", "chunk_count": len(chunks)}).Error
	})
	if err != nil {
		return fmt.Errorf("写库失败(文档 %d): %w", docID, err)
	}

	// 增量更新内存向量缓存(与事务一致)
	s.mu.Lock()
	for i, row := range created {
		s.vecs[row.ID] = vecs[i]
	}
	s.mu.Unlock()
	return nil
}

// Delete 级联删除文档及其 chunks,并清理向量缓存(§8.5)。
func (s *Service) Delete(ctx context.Context, docID uint64) error {
	var ids []uint64
	if err := s.db.WithContext(ctx).Model(&model.KbChunk{}).
		Where("doc_id=?", docID).Pluck("id", &ids).Error; err != nil {
		return fmt.Errorf("查询 chunks: %w", err)
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("doc_id=?", docID).Delete(&model.KbChunk{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.KbDocument{}, docID).Error
	})
	if err != nil {
		return fmt.Errorf("删除文档 %d: %w", docID, err)
	}
	s.mu.Lock()
	for _, id := range ids {
		delete(s.vecs, id)
	}
	s.mu.Unlock()
	return nil
}

// Search 混合检索:向量路 + FULLTEXT 关键词路各取 topK*2 候选,
// 用 RRF(Reciprocal Rank Fusion)按"排名"融合(而非直接比较异度量纲的原始分数),
// 两条路的候选取并集后排序,取 topK 返回。实现 tools.KBSearcher。
func (s *Service) Search(ctx context.Context, query string, topK int) (string, error) {
	if topK <= 0 {
		topK = s.topK
	}
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("query 不能为空")
	}
	queryVec, err := s.embed.Embed(ctx, []string{query})
	if err != nil {
		return "", fmt.Errorf("查询向量化失败: %w", err)
	}

	// ── 混合检索融合策略(手动切换,注释一行即可对比效果)──────────
	// 默认 searchRRF(新版):两路候选并集,按 RRF 排名融合,纯关键词命中也能进入结果。
	// 对比 searchWeighted(加权版,已修"关键词不补漏"的 bug):两路并集,得分 = 向量分 + 关键词命中 +0.4。
	// 两套差异只在"融合方式":加权直接加异度量纲的分数,RRF 只按排名融合。
	items := s.searchRRF(ctx, queryVec[0], query, topK)
	// items := s.searchWeighted(ctx, queryVec[0], query, topK)
	// ───────────────────────────────────────────────────────────
	return formatHits(items), nil
}

// searchRRF 新版混合检索:向量路 + FULLTEXT 关键词路各取 topK*2,
// 用 RRF 按"排名"融合(见 rrfFuse),两路候选并集排序后取 topK。
func (s *Service) searchRRF(ctx context.Context, queryVec []float32, query string, topK int) []searchItem {
	vecHits := s.vectorSearch(queryVec, topK*2) // 向量路:按余弦降序(rank 1..N)
	kwHits := s.keywordSearch(ctx, query, topK*2)
	fused := rrfFuse(vecHits, kwHits)
	if len(fused) > topK {
		fused = fused[:topK]
	}
	return s.loadItems(ctx, fused)
}

// searchWeighted 加权混合检索(与 RRF 对比用,已修"关键词不补漏"的 bug):
// 向量与关键词两路候选取并集(见 loadItemsWeighted),得分 = 向量余弦分
// (纯关键词命中按 0) + 关键词命中 ? keywordBoost : 0(见 rankItemsWeighted)。
// 与 searchRRF 的差异只在融合方式:加权把异度量纲的分数直接相加,RRF 只按排名融合。
func (s *Service) searchWeighted(ctx context.Context, queryVec []float32, query string, topK int) []searchItem {
	vecScores := s.vectorSearchScores(queryVec, topK*2)
	kwSet := s.keywordSearchSet(ctx, query, topK*2)
	items := s.loadItemsWeighted(ctx, vecScores, kwSet)
	return rankItemsWeighted(items, topK)
}

// hit 一条检索命中:id 为 chunkID,rank 为它在当前路径内的排名(1 起)。
type hit struct {
	id   uint64
	rank int
}

// fusedHit RRF 融合后的候选。
type fusedHit struct {
	id    uint64
	score float64
}

// rrfK RRF 平滑常数(标准推荐 60)。k 越大,名次差异带来的分数差越小。
const rrfK = 60

// rrfFuse 倒数排名融合:对每条在任意路径出现的 chunk,得分 = Σ 1/(rrfK + rank)。
// 向量余弦与 FULLTEXT 相关度量纲不同,直接相加不科学;RRF 只比较"排第几"。
// 同一 chunk 在两条路都命中则分数叠加,因此两路的候选都能进入最终结果。
func rrfFuse(lists ...[]hit) []fusedHit {
	scores := make(map[uint64]float64)
	for _, list := range lists {
		for _, h := range list {
			scores[h.id] += 1.0 / float64(rrfK+h.rank)
		}
	}
	out := make([]fusedHit, 0, len(scores))
	for id, sc := range scores {
		out = append(out, fusedHit{id: id, score: sc})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].score > out[j].score })
	return out
}

// GetDocument 返回文档全文(按 seq 拼接 chunks)。实现 tools.KBSearcher。
// GetDocument 返回文档全文(按 seq 拼接 chunks)。
// 注意:切块时相邻块会补 overlap 前缀(chunkText),直接拼接会导致重叠文本重复,
// 因此这里用 joinChunks 去掉 overlap,还原接近原文的全文。
func (s *Service) GetDocument(ctx context.Context, docID uint64) (string, error) {
	var doc model.KbDocument
	if err := s.db.WithContext(ctx).First(&doc, docID).Error; err != nil {
		return "", fmt.Errorf("文档不存在: %d", docID)
	}
	var rows []model.KbChunk
	if err := s.db.WithContext(ctx).Where("doc_id=?", docID).Order("seq ASC").Find(&rows).Error; err != nil {
		return "", fmt.Errorf("读取文档 %d: %w", docID, err)
	}
	body := joinChunks(rows, s.overlapChars)
	if body == "" {
		return "", fmt.Errorf("文档 %d 无内容", docID)
	}
	return fmt.Sprintf("文档: %s\n%s", doc.Title, body), nil
}

// joinChunks 按 seq 顺序拼接 chunks,去掉切块时补入的 overlap 前缀。
// 背景:chunkText 会给相邻块补 "lastChars(prev, overlap) + \n" 前缀,
// 若直接拼接全文,重叠部分会重复出现(浪费 token、内容不干净)。
// 这里对每个非首块,若其头部命中上一块末尾的 overlap 文本,则剥掉该前缀。
func joinChunks(rows []model.KbChunk, overlapChars int) string {
	var b strings.Builder
	var prevPart string // 上一块"剥离 overlap 后"的内容,即切块前的原始块
	for i, r := range rows {
		text := r.Content
		if i > 0 && overlapChars > 0 && prevPart != "" {
			// chunkText 给 chunks[i] 补的前缀是 lastChars(原始 chunks[i-1], overlap),
			// 因此用"剥离后的上一块"而非"存库的上一块"推导,否则上一块短于 overlap 时会对不上。
			overlap := lastChars(prevPart, overlapChars)
			if overlap != "" && strings.HasPrefix(text, overlap) {
				text = text[len(overlap):]
				text = strings.TrimPrefix(text, "\n")
			}
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(text)
		prevPart = text
	}
	return strings.TrimSpace(b.String())
}

// vectorSearch 全量内存余弦,返回 topN 命中(按相似度降序,rank 从 1 起)。
func (s *Service) vectorSearch(queryVec []float32, topN int) []hit {
	s.mu.RLock()
	defer s.mu.RUnlock()
	type scored struct {
		id    uint64
		score float64
	}
	all := make([]scored, 0, len(s.vecs))
	for id, vec := range s.vecs {
		all = append(all, scored{id, dot(queryVec, vec)})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].score > all[j].score })
	if len(all) > topN {
		all = all[:topN]
	}
	out := make([]hit, len(all))
	for i, sc := range all {
		out[i] = hit{id: sc.id, rank: i + 1}
	}
	return out
}

// keywordSearch FULLTEXT 关键词检索,返回 topN 命中(按相关度降序,rank 从 1 起)。
// 中文文档依赖 ngram 全文索引(见 store.NewMySQL 的迁移)。
func (s *Service) keywordSearch(ctx context.Context, query string, limit int) []hit {
	terms := keywordTerms(query)
	if len(terms) == 0 {
		return nil
	}
	against := strings.Join(terms, " ")
	var ids []uint64
	err := s.db.WithContext(ctx).Raw(
		"SELECT id FROM kb_chunks WHERE MATCH(content) AGAINST(? IN NATURAL LANGUAGE MODE) "+
			"ORDER BY MATCH(content) AGAINST(? IN NATURAL LANGUAGE MODE) DESC LIMIT ?",
		against, against, limit,
	).Scan(&ids).Error
	if err != nil {
		slog.Warn("FULLTEXT 检索失败,降级为仅向量检索", "err", err)
		return nil
	}
	out := make([]hit, len(ids))
	for i, id := range ids {
		out[i] = hit{id: id, rank: i + 1}
	}
	return out
}

// loadItems 加载融合后 topK 候选的 meta(文档ID/序号/内容),保持融合排序。
// 注:旧实现只加载"向量候选",纯关键词命中进不了最终结果;
// 现在两路候选取并集后融合,关键词路真正起到补漏作用。
func (s *Service) loadItems(ctx context.Context, fused []fusedHit) []searchItem {
	if len(fused) == 0 {
		return nil
	}
	ids := make([]uint64, 0, len(fused))
	for _, f := range fused {
		ids = append(ids, f.id)
	}
	var rows []model.KbChunk
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		slog.Warn("加载候选 chunk 失败", "err", err)
		return nil
	}
	byID := make(map[uint64]model.KbChunk, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	items := make([]searchItem, 0, len(fused))
	for _, f := range fused {
		r, ok := byID[f.id]
		if !ok {
			continue
		}
		items = append(items, searchItem{
			chunkID: r.ID, docID: r.DocID, seq: r.Seq, content: r.Content,
		})
	}
	return items
}

// ═══ 加权检索实现(已修 bug 版):与 RRF 共存,仅用于对比效果 ═══

// vectorSearchScores 旧版向量检索:全量内存余弦,返回 topN chunkID -> 相似度。
func (s *Service) vectorSearchScores(queryVec []float32, topN int) map[uint64]float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	type scored struct {
		id    uint64
		score float64
	}
	all := make([]scored, 0, len(s.vecs))
	for id, vec := range s.vecs {
		all = append(all, scored{id, dot(queryVec, vec)})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].score > all[j].score })
	if len(all) > topN {
		all = all[:topN]
	}
	out := make(map[uint64]float64, len(all))
	for _, sc := range all {
		out[sc.id] = sc.score
	}
	return out
}

// keywordSearchSet 旧版关键词检索:返回命中的 chunkID 集合(不保序)。
func (s *Service) keywordSearchSet(ctx context.Context, query string, limit int) map[uint64]bool {
	terms := keywordTerms(query)
	if len(terms) == 0 {
		return nil
	}
	against := strings.Join(terms, " ")
	var ids []uint64
	err := s.db.WithContext(ctx).Raw(
		"SELECT id FROM kb_chunks WHERE MATCH(content) AGAINST(? IN NATURAL LANGUAGE MODE) "+
			"ORDER BY MATCH(content) AGAINST(? IN NATURAL LANGUAGE MODE) DESC LIMIT ?",
		against, against, limit,
	).Scan(&ids).Error
	if err != nil {
		slog.Warn("FULLTEXT 检索失败,降级为仅向量检索", "err", err)
		return nil
	}
	out := make(map[uint64]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// loadItemsWeighted 加权路径候选加载:取"向量候选 ∪ 关键词候选"的并集,
// 保证纯关键词命中也能进入最终结果(与 RRF 路径一致,修复了"关键词不补漏"的问题)。
// vecScore 对纯关键词命中的 chunk 为 0(它没有向量分,只能靠 keywordBoost 参与排序)。
func (s *Service) loadItemsWeighted(ctx context.Context, vecScores map[uint64]float64, kwSet map[uint64]bool) []searchItem {
	idSet := make(map[uint64]struct{}, len(vecScores)+len(kwSet))
	for id := range vecScores {
		idSet[id] = struct{}{}
	}
	for id := range kwSet {
		idSet[id] = struct{}{}
	}
	if len(idSet) == 0 {
		return nil
	}
	ids := make([]uint64, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	var rows []model.KbChunk
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		slog.Warn("加载候选 chunk 失败", "err", err)
		return nil
	}
	items := make([]searchItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, searchItem{
			chunkID: r.ID, docID: r.DocID, seq: r.Seq, content: r.Content,
			vecScore: vecScores[r.ID], hasKeyword: kwSet[r.ID],
		})
	}
	return items
}

// keywordBoost 旧版关键词命中加分。
const keywordBoost = 0.4

// rankItemsWeighted 旧版排序:合并同 doc_id+seq(取较大向量分),向量分 + 关键词命中加分,取 topK。
func rankItemsWeighted(items []searchItem, topK int) []searchItem {
	byKey := map[string]*searchItem{}
	for i := range items {
		key := fmt.Sprintf("%d-%d", items[i].docID, items[i].seq)
		if ex, ok := byKey[key]; ok {
			if items[i].vecScore > ex.vecScore {
				ex.vecScore = items[i].vecScore
			}
			ex.hasKeyword = ex.hasKeyword || items[i].hasKeyword
			continue
		}
		item := items[i]
		byKey[key] = &item
	}
	list := make([]searchItem, 0, len(byKey))
	for _, it := range byKey {
		if it.hasKeyword {
			it.vecScore += keywordBoost
		}
		list = append(list, *it)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].vecScore > list[j].vecScore })
	if len(list) > topK {
		list = list[:topK]
	}
	return list
}

// keywordTerms 把查询切成关键词(按非字母数字切分,保留中文字符)。
func keywordTerms(query string) []string {
	fields := strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	terms := make([]string, 0, len(fields))
	for _, f := range fields {
		if f != "" {
			terms = append(terms, f)
		}
	}
	return terms
}

// searchItem 检索结果条目(供 formatHits 输出引用)。
type searchItem struct {
	chunkID uint64
	docID   uint64
	seq     int
	content string
	// 以下两字段仅旧版加权检索(searchWeighted)使用,RRF 路径保持零值。
	vecScore   float64
	hasKeyword bool
}

// formatHits 把结果格式化为"命中片段 + 引用",供 LLM 作答时引用。
func formatHits(items []searchItem) string {
	if len(items) == 0 {
		return "未在知识库中找到相关内容"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "共 %d 条相关片段:\n", len(items))
	for i, it := range items {
		content := it.content
		if runeLen(content) > 200 { // 按字符截断,避免切断多字节 UTF-8 产生非法字节
			content = truncateChars(content, 200) + "..."
		}
		fmt.Fprintf(&b, "[%d] (文档%d-第%d段) %s\n", i+1, it.docID, it.seq, content)
	}
	return b.String()
}
