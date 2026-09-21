// Package context 上下文管理:token 估算、消息窗口构建、超阈值压缩(§8.4)。
package context

import (
	"log/slog"
	"math"
	"strings"
	"sync"
	"unicode"

	"agent-platform/internal/agent/llm"
)

// TokenEstimator token 估算接口,便于替换估算算法。
type TokenEstimator interface {
	// Count 估算单段文本的 token 数。
	Count(text string) int
	// CountMessages 估算一组消息的 token 数(含角色开销近似)。
	CountMessages(msgs []llm.Message) int
}

// CalibratableEstimator 支持用"真实 token 用量"自我校准的估算器。
type CalibratableEstimator interface {
	TokenEstimator
	// Calibrate 用一次真实调用结果校准系数:
	// estimated = 本次调用前估算器给出的估计值,actual = 服务端返回的真实 prompt token 数。
	Calibrate(estimated, actual int)
	// Coefficient 返回当前系数与参与校准的样本数(观测/调试用)。
	Coefficient() (float64, int)
}

// ============================================================================
// 基础估算:启发式(纯本地,零外部数据、零网络)
// ============================================================================

// HeuristicEstimator 按字符类型粗略估算 token 数。
//
// 为什么不用 tiktoken:
//   - cl100k_base 的编码表(约 1.7MB)不在库内,首次使用会去 Azure Blob 下载,
//     该域名在国内经常不可达,曾导致服务启动直接失败;
//   - token 估算在本项目里只用于"判断上下文是否超过阈值",精度要求不高,
//     再配合下面的动态校准即可逼近真实值。
//
// 估算规则(宁可略高估,避免超出上下文窗口):
//   - ASCII(英文/数字/符号) ≈ 4 字符 / token
//   - CJK(中日韩文字)        ≈ 1 字 / token
//   - 其他                    ≈ 2 字符 / token
type HeuristicEstimator struct{}

// Count 估算单段文本 token 数。
func (HeuristicEstimator) Count(text string) int {
	var ascii, cjk, other int
	for _, r := range text {
		switch {
		case r < 0x80:
			ascii++
		case unicode.Is(unicode.Han, r) ||
			unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) ||
			unicode.Is(unicode.Hangul, r):
			cjk++
		default:
			other++
		}
	}
	n := ascii/4 + cjk + other/2
	if n < 1 {
		n = 1 // 非空文本至少算 1 个 token
	}
	return n
}

// CountMessages 估算消息列表:每条消息固定开销 + 内容 + 工具调用参数。
// 固定开销 3 对齐 OpenAI 官方的 role/分隔符近似值。
func (h HeuristicEstimator) CountMessages(msgs []llm.Message) int {
	total := 0
	for _, m := range msgs {
		total += 3
		total += h.Count(m.Content)
		for _, tc := range m.ToolCalls {
			total += h.Count(tc.Function.Name) + h.Count(tc.Function.Arguments)
		}
	}
	return total
}

// ============================================================================
// 校准估算器:启发式 + 用真实用量动态修正系数
// ============================================================================

// 校准参数。
const (
	defaultCoef   = 1.0  // 初始系数:等价于纯启发式
	defaultAlpha  = 0.2  // EWMA 权重:新样本占 20%,收敛平稳不抖动
	minSampleSize = 20   // 估算值小于该值的样本不参与校准(短文本比值噪声大)
	minRatio      = 0.25 // 单次样本比值下限(防异常值带飞系数)
	maxRatio      = 4.0  // 单次样本比值上限
)

// CalibratedEstimator 在启发式估算之上,用 LLM 返回的真实 usage 动态校准。
//
// 原理:
//
//	估算值 × 系数 ≈ 真实值
//	每次调用后取 ratio = 真实 prompt_tokens / 本次估算值,
//	再用 EWMA(指数加权移动平均)平滑更新系数:
//	    系数 = (1-α) × 系数 + α × ratio
//
// 价值:启发式估算的系统性偏差(中英混排、代码、工具定义、消息格式开销等)
// 会被真实数据自动吸收并修正,因此既不依赖外网,又能逐步逼近服务商的分词口径。
type CalibratedEstimator struct {
	base  HeuristicEstimator
	mu    sync.RWMutex
	coef  float64
	alpha float64
	n     int // 参与过校准的样本数
}

// NewCalibratedEstimator 创建带校准的估算器,初始系数为 1(纯启发式)。
func NewCalibratedEstimator() *CalibratedEstimator {
	return &CalibratedEstimator{coef: defaultCoef, alpha: defaultAlpha}
}

// Count 估算单段文本 token 数(已应用校准系数)。
func (c *CalibratedEstimator) Count(text string) int {
	return c.scale(c.base.Count(text))
}

// CountMessages 估算消息列表 token 数(已应用校准系数)。
func (c *CalibratedEstimator) CountMessages(msgs []llm.Message) int {
	return c.scale(c.base.CountMessages(msgs))
}

// Calibrate 用一次真实调用结果更新系数。
// 样本过小或用量缺失时直接跳过,避免噪声污染系数。
func (c *CalibratedEstimator) Calibrate(estimated, actual int) {
	if actual <= 0 || estimated < minSampleSize {
		return
	}
	ratio := float64(actual) / float64(estimated)
	if ratio < minRatio {
		ratio = minRatio
	}
	if ratio > maxRatio {
		ratio = maxRatio
	}
	c.mu.Lock()
	c.coef = (1-c.alpha)*c.coef + c.alpha*ratio
	c.n++
	c.mu.Unlock()
}

// Coefficient 返回当前系数与样本数。
func (c *CalibratedEstimator) Coefficient() (float64, int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.coef, c.n
}

// scale 把基础估算值按系数缩放,结果至少为 1。
func (c *CalibratedEstimator) scale(base int) int {
	if base <= 0 {
		return 0
	}
	c.mu.RLock()
	coef := c.coef
	c.mu.RUnlock()
	n := int(math.Round(float64(base) * coef))
	if n < 1 {
		n = 1
	}
	return n
}

// ============================================================================
// 工厂
// ============================================================================

// NewEstimator 按配置创建 token 估算器:
//
//	"calibrated"(默认) 启发式估算 + 真实用量自动校准
//	"heuristic"       纯启发式估算,不校准(便于对照/排查)
//
// 重要:两种模式都完全本地运行,不会产生任何网络请求。
// 校准样本来自正常对话调用返回的 usage,属于"顺手采集",不额外发起请求。
func NewEstimator(kind string) TokenEstimator {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "heuristic", "simple":
		slog.Info("[context] token 估算器:启发式(不校准)")
		return HeuristicEstimator{}
	case "", "auto", "calibrated":
		slog.Info("[context] token 估算器:启发式 + 真实用量自动校准")
		return NewCalibratedEstimator()
	default:
		slog.Warn("[context] 未知 estimator 配置,使用默认校准模式", "value", kind)
		return NewCalibratedEstimator()
	}
}
