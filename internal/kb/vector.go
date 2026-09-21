package kb

import (
	"encoding/binary"
	"math"
)

// encodeVector 把 float32 向量序列化为小端字节,存入 BLOB。
func encodeVector(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// decodeVector 从 BLOB 反序列化 float32 向量。
func decodeVector(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

// dot 点积(归一化向量下等价于余弦相似度)。
func dot(a, b []float32) float64 {
	var s float64
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}
