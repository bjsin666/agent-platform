package kb

import (
	"regexp"
	"strings"
)

// chunk 文本块。
type chunk struct {
	seq  int
	text string
}

// clean 清洗 markdown 噪声:去掉图片语法、把链接语法还原为纯文本。
func clean(text string) string {
	// ![alt](url) 整段去掉
	text = reImg.ReplaceAllString(text, "")
	// [text](url) -> text
	text = reLink.ReplaceAllString(text, "$1")
	return text
}

var (
	reImg  = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	reLink = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
)

// chunkText 切块(§8.5):
// 1) 优先按 Markdown 标题(### 及以上)切分;
// 2) 单块 >maxChars 递归按段落切,段落仍超限则硬切;
// 3) 相邻块按 overlap 字数补重叠(保证语义连续)。
// 长度全部按字符(rune)计,避免按字节切片切坏 UTF-8 中文字符。
func chunkText(text string, maxChars, overlap int) []chunk {
	var chunks []string
	for _, sec := range splitSections(text) {
		if runeLen(sec) <= maxChars {
			chunks = append(chunks, sec)
			continue
		}
		chunks = append(chunks, splitLongSection(sec, maxChars)...)
	}
	// 相邻块补重叠(取前块末尾 overlap 个字符)
	if overlap > 0 {
		for i := len(chunks) - 1; i > 0; i-- {
			chunks[i] = lastChars(chunks[i-1], overlap) + "\n" + chunks[i]
		}
	}
	out := make([]chunk, len(chunks))
	for i, c := range chunks {
		out[i] = chunk{seq: i, text: c}
	}
	return out
}

// splitSections 先按标题(#/##/###)切段,段内再按空行段落切。
func splitSections(text string) []string {
	var sections []string
	var secBuf []string
	flush := func() {
		s := strings.TrimSpace(strings.Join(secBuf, "\n"))
		if s != "" {
			sections = append(sections, s)
		}
		secBuf = nil
	}
	for _, ln := range strings.Split(text, "\n") {
		if isHeading(ln) {
			flush()
		}
		secBuf = append(secBuf, ln)
	}
	flush()
	return sections
}

// splitLongSection 超长段落内按空行段落切;段落仍超限则按字符硬切。
func splitLongSection(sec string, maxChars int) []string {
	var chunks []string
	var cur strings.Builder
	curLen := 0
	for _, para := range strings.Split(sec, "\n\n") {
		p := strings.TrimSpace(para)
		if p == "" {
			continue
		}
		// 段落超长硬切(按字符)
		for runeLen(p) > maxChars {
			if curLen > 0 {
				chunks = append(chunks, cur.String())
				cur.Reset()
				curLen = 0
			}
			head := truncateChars(p, maxChars)
			chunks = append(chunks, head)
			p = p[len(head):] // head 的字节长度即前 maxChars 个字符的字节偏移
		}
		if curLen > 0 && curLen+1+runeLen(p) > maxChars {
			chunks = append(chunks, cur.String())
			cur.Reset()
			curLen = 0
		}
		if curLen > 0 {
			cur.WriteByte('\n')
			curLen++
		}
		cur.WriteString(p)
		curLen += runeLen(p)
	}
	if curLen > 0 {
		chunks = append(chunks, cur.String())
	}
	return chunks
}

// isHeading 是否为 Markdown 标题(### 及以上:即 1~3 级)。
func isHeading(ln string) bool {
	t := strings.TrimSpace(ln)
	for _, prefix := range []string{"### ", "## ", "# "} {
		if strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return false
}

// runeLen 字符数(按 rune,非字节)。
func runeLen(s string) int {
	return len([]rune(s))
}

// truncateChars 取前 n 个字符;不足 n 个则原样返回。
func truncateChars(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// lastChars 取最后 n 个字符;不足 n 个则原样返回。
func lastChars(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}
