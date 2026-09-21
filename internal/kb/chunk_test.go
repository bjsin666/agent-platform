package kb

import (
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"agent-platform/internal/model"
)

// TestClean 清洗 markdown 图片与链接噪声。
func TestClean(t *testing.T) {
	in := "参考 ![图](http://x/a.png) 与 [官方文档](http://doc)。"
	out := clean(in)
	if strings.Contains(out, "![") || strings.Contains(out, "(http") {
		t.Fatalf("清洗后仍含噪声: %q", out)
	}
	if !strings.Contains(out, "官方文档") {
		t.Fatalf("链接文本应保留: %q", out)
	}
}

// TestChunkByHeading 按标题切块。
func TestChunkByHeading(t *testing.T) {
	doc := "# 第一章\n内容一\n## 第二章\n内容二\n### 第三章\n内容三"
	chunks := chunkText(doc, 1000, 0)
	if len(chunks) != 3 {
		t.Fatalf("应按标题切成 3 块,实际 %d", len(chunks))
	}
	if !strings.Contains(chunks[0].text, "第一章") || !strings.Contains(chunks[2].text, "第三章") {
		t.Fatalf("块内容错误: %+v", chunks)
	}
	if chunks[0].seq != 0 || chunks[2].seq != 2 {
		t.Fatalf("seq 应递增: %+v", chunks)
	}
}

// TestChunkMaxChars 超长块按段落/硬切,每块不超过上限。
func TestChunkMaxChars(t *testing.T) {
	// 构造 500 个"段"的超长段落文本
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		sb.WriteString(strings.Repeat("字", 30))
		sb.WriteString("\n\n")
	}
	text := sb.String()
	chunks := chunkText(text, 100, 10)
	if len(chunks) < 2 {
		t.Fatalf("应切成多块,实际 %d", len(chunks))
	}
	for _, c := range chunks {
		if len([]rune(c.text)) > 130 { // 上限 100 + overlap 10 + 换行容差
			t.Fatalf("块超长: %d 字符", len([]rune(c.text)))
		}
	}
}

// TestChunkUTF8Safe 切块不能切坏 UTF-8 中文字符(按字节切会产生非法字节)。
func TestChunkUTF8Safe(t *testing.T) {
	// 构造 500 个连续中文字符的文档
	text := strings.Repeat("字", 500)
	chunks := chunkText(text, 100, 20)
	if len(chunks) < 2 {
		t.Fatalf("应切成多块")
	}
	for _, c := range chunks {
		if !utf8.ValidString(c.text) {
			t.Fatalf("块含非法 UTF-8: %q", c.text)
		}
	}
}

// TestChunkOverlap 相邻块重叠 overlap 字。
func TestChunkOverlap(t *testing.T) {
	doc := "# 标题\n" + strings.Repeat("甲", 200) + "\n" + strings.Repeat("乙", 200)
	chunks := chunkText(doc, 100, 20)
	if len(chunks) < 2 {
		t.Fatalf("应切成多块")
	}
	// 后一块应包含前一块末尾的 overlap 字符(此处"甲")
	if !strings.Contains(chunks[1].text, "甲") {
		t.Fatalf("后块应含前块重叠内容: %q", chunks[1].text[:50])
	}
}

// TestChunkEmpty 空文本不产生块。
func TestChunkEmpty(t *testing.T) {
	if got := chunkText("   \n\n  ", 100, 0); len(got) != 0 {
		t.Fatalf("空文本应无块,实际 %d", len(got))
	}
}

// TestVectorRoundtrip 向量编码/解码往返一致。
func TestVectorRoundtrip(t *testing.T) {
	v := []float32{0.1, -0.5, 1.0, 0.0, 3.14159}
	got := decodeVector(encodeVector(v))
	if len(got) != len(v) {
		t.Fatalf("长度不一致")
	}
	for i := range v {
		if got[i] != v[i] {
			t.Fatalf("第 %d 维不一致: %v != %v", i, got[i], v[i])
		}
	}
}

// TestDot 归一化向量点积近似余弦。
func TestDot(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{0, 1}
	if d := dot(a, b); math.Abs(d) > 1e-9 {
		t.Fatalf("正交向量点积应为 0: %v", d)
	}
	if d := dot([]float32{1, 0}, []float32{1, 0}); math.Abs(d-1) > 1e-9 {
		t.Fatalf("相同向量点积应为 1: %v", d)
	}
}

// TestRRFFuse RRF 融合:双路命中的 chunk 分数叠加并排最前;单路按名次排序。
func TestRRFFuse(t *testing.T) {
	vec := []hit{{id: 1, rank: 1}, {id: 2, rank: 2}, {id: 3, rank: 3}}
	kw := []hit{{id: 2, rank: 1}, {id: 4, rank: 2}}
	fused := rrfFuse(vec, kw)
	if len(fused) != 4 {
		t.Fatalf("两路候选并集应为 4 条,实际 %d", len(fused))
	}
	// id=2 双路命中:1/(60+2) + 1/(60+1),应为最高
	if fused[0].id != 2 {
		t.Fatalf("双路命中的 chunk 应排第一: %+v", fused)
	}
	want := 1.0/62 + 1.0/61
	if math.Abs(fused[0].score-want) > 1e-12 {
		t.Fatalf("id=2 得分错误: got %v want %v", fused[0].score, want)
	}
	// 单路按名次: id1(1/61) > id4(1/62) > id3(1/63)
	if fused[1].id != 1 || fused[2].id != 4 || fused[3].id != 3 {
		t.Fatalf("RRF 单路排序错误: %+v", fused)
	}
}

// TestRRFFuseEmpty 两路都空返回空。
func TestRRFFuseEmpty(t *testing.T) {
	if got := rrfFuse(nil, nil); len(got) != 0 {
		t.Fatalf("空输入应返回空,实际 %+v", got)
	}
}

// TestFormatHits 命中为空时给出提示。
func TestFormatHits(t *testing.T) {
	if !strings.Contains(formatHits(nil), "未在知识库") {
		t.Fatal("空结果应提示")
	}
	s := formatHits([]searchItem{{docID: 5, seq: 2, content: "内容"}})
	if !strings.Contains(s, "(文档5-第2段)") {
		t.Fatalf("引用格式错误: %q", s)
	}
}

// TestJoinChunksNoOverlap 拼接全文时去掉 overlap 前缀,避免重叠文本重复。
func TestJoinChunksNoOverlap(t *testing.T) {
	doc := "# 标题\n## 第一段\n" + strings.Repeat("甲", 100) +
		"\n## 第二段\n" + strings.Repeat("乙", 100)
	chunks := chunkText(doc, 1000, 20) // 每个标题节都小于上限,只发生 overlap 补缀
	if len(chunks) != 3 {
		t.Fatalf("应按标题切成 3 块,实际 %d", len(chunks))
	}
	rows := make([]model.KbChunk, len(chunks))
	for i, c := range chunks {
		rows[i] = model.KbChunk{Seq: c.seq, Content: c.text}
	}
	joined := joinChunks(rows, 20)

	want := "# 标题\n\n## 第一段\n" + strings.Repeat("甲", 100) +
		"\n\n## 第二段\n" + strings.Repeat("乙", 100)
	if joined != want {
		t.Fatalf("拼接结果与预期不符:\n got: %q\nwant: %q", joined, want)
	}
	// 上一块末尾的 overlap 片段不应在拼接结果中重复出现
	if strings.Count(joined, "甲") != 100 || strings.Count(joined, "乙") != 100 {
		t.Fatalf("重叠内容重复:甲=%d 乙=%d", strings.Count(joined, "甲"), strings.Count(joined, "乙"))
	}
}

// TestJoinChunksOrdered 无 overlap 时按 seq 顺序拼接且不丢内容。
func TestJoinChunksOrdered(t *testing.T) {
	chunks := chunkText("## 一\n内容A\n## 二\n内容B", 1000, 0)
	rows := make([]model.KbChunk, len(chunks))
	for i, c := range chunks {
		rows[i] = model.KbChunk{Seq: c.seq, Content: c.text}
	}
	joined := joinChunks(rows, 0)
	if !strings.Contains(joined, "内容A") || !strings.Contains(joined, "内容B") {
		t.Fatalf("拼接丢失内容: %q", joined)
	}
	if strings.Index(joined, "内容A") > strings.Index(joined, "内容B") {
		t.Fatalf("顺序错误: %q", joined)
	}
}

// TestFormatHitsUTF8Safe 命中片段按字符截断,不产生非法 UTF-8(修复 content[:200] 字节截断)。
func TestFormatHitsUTF8Safe(t *testing.T) {
	long := strings.Repeat("甲乙丙丁", 100) // 400 个中文字符,截断点必落在多字节字符内
	s := formatHits([]searchItem{{docID: 1, seq: 0, content: long}})
	if !utf8.ValidString(s) {
		t.Fatalf("截断产生非法 UTF-8: %q", s)
	}
	if !strings.Contains(s, "...") {
		t.Fatalf("应含截断标记: %q", s)
	}
}

// TestJoinChunksShortPrev 前块短于 overlap(如标题块)时,拼接仍要去掉重叠前缀。
func TestJoinChunksShortPrev(t *testing.T) {
	doc := "# 标题\n## 第一段\n" + strings.Repeat("甲", 100) +
		"\n## 第二段\n" + strings.Repeat("乙", 100)
	chunks := chunkText(doc, 1000, 50) // overlap 50 > 标题块长度
	rows := make([]model.KbChunk, len(chunks))
	for i, c := range chunks {
		rows[i] = model.KbChunk{Seq: c.seq, Content: c.text}
	}
	joined := joinChunks(rows, 50)
	if strings.Count(joined, "## 第一段") != 1 || strings.Count(joined, "## 第二段") != 1 {
		t.Fatalf("标题/小节重复: %q", joined)
	}
	if strings.Count(joined, "甲") != 100 || strings.Count(joined, "乙") != 100 {
		t.Fatalf("内容重复:甲=%d 乙=%d", strings.Count(joined, "甲"), strings.Count(joined, "乙"))
	}
}

// TestRankItemsWeighted 加权排序(已修 bug 版):向量分 + 关键词加分;
// 纯关键词命中(vecScore=0)靠 keywordBoost 也能浮到弱向量命中之上。
func TestRankItemsWeighted(t *testing.T) {
	items := []searchItem{
		{chunkID: 1, docID: 1, seq: 0, content: "a", vecScore: 0.8},
		{chunkID: 2, docID: 1, seq: 0, content: "a", vecScore: 0.6, hasKeyword: true}, // 同 doc+seq 应合并
		{chunkID: 3, docID: 1, seq: 1, content: "b", vecScore: 0.9},
		{chunkID: 4, docID: 2, seq: 0, content: "c", vecScore: 0.2},
		{chunkID: 5, docID: 3, seq: 0, content: "d", hasKeyword: true}, // 纯关键词命中:0 + 0.4
	}
	ranked := rankItemsWeighted(items, 4)
	if len(ranked) != 4 {
		t.Fatalf("应去重后取 4 条,实际 %d", len(ranked))
	}
	// doc1-seq0 合并后带关键词加成:max(0.8,0.6)+0.4=1.2,应为第 0 名
	if ranked[0].docID != 1 || ranked[0].seq != 0 || ranked[0].vecScore < 1.1 {
		t.Fatalf("关键词加成/排序错误: %+v", ranked[0])
	}
	if ranked[1].docID != 1 || ranked[1].seq != 1 {
		t.Fatalf("排序错误: %+v", ranked)
	}
	// 纯关键词命中(doc3,0.4)应排在弱向量命中(doc2,0.2)之前——证明并集后关键词能补漏
	if ranked[2].docID != 3 || ranked[2].seq != 0 {
		t.Fatalf("纯关键词命中应排在弱向量命中之前: %+v", ranked)
	}
	if ranked[3].docID != 2 || ranked[3].seq != 0 {
		t.Fatalf("排序错误: %+v", ranked)
	}
}
