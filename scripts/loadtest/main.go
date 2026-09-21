// 压测脚本:并发请求指定端点,统计 QPS 与延迟分位。结果真实记录,不伪造(§13.3)。
//
// 用法:
//
//	go run ./scripts/loadtest -url http://127.0.0.1:8080/health -c 20 -n 500
//	go run ./scripts/loadtest -url http://127.0.0.1:8080/v1/sessions -c 5 -n 50 -method POST -body '{"title":"压测"}'
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:8080/health", "目标 URL")
	concurrency := flag.Int("c", 10, "并发数")
	total := flag.Int("n", 200, "总请求数")
	method := flag.String("method", "GET", "HTTP 方法")
	body := flag.String("body", "", "请求体(JSON)")
	apiKey := flag.String("key", "", "API Key(Bearer)")
	flag.Parse()

	if *concurrency <= 0 || *total <= 0 {
		fmt.Println("并发数与总请求数必须为正")
		os.Exit(1)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	sem := make(chan struct{}, *concurrency)
	var (
		mu        sync.Mutex
		latencies []time.Duration
		statuses  = map[int]int{}
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < *total; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			req, err := http.NewRequestWithContext(ctx, *method, *url, bytes.NewReader([]byte(*body)))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			if *apiKey != "" {
				req.Header.Set("Authorization", "Bearer "+*apiKey)
			}
			reqStart := time.Now()
			resp, err := client.Do(req)
			lat := time.Since(reqStart)
			mu.Lock()
			if err != nil {
				statuses[0]++
			} else {
				statuses[resp.StatusCode]++
				resp.Body.Close()
				latencies = append(latencies, lat)
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	// 统计
	p := func(q float64) time.Duration {
		if len(latencies) == 0 {
			return 0
		}
		sorted := append([]time.Duration(nil), latencies...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		idx := int(float64(len(sorted)) * q)
		if idx >= len(sorted) {
			idx = len(sorted) - 1
		}
		return sorted[idx]
	}

	fmt.Println("=== 压测结果(真实记录) ===")
	fmt.Printf("环境: url=%s method=%s 并发=%d 总请求=%d\n", *url, *method, *concurrency, *total)
	fmt.Printf("参数: 总耗时=%s QPS=%.1f\n", elapsed.Round(time.Millisecond), float64(*total)/elapsed.Seconds())
	fmt.Printf("延迟: p50=%s p95=%s p99=%s\n", p(0.5).Round(time.Microsecond), p(0.95).Round(time.Microsecond), p(0.99).Round(time.Microsecond))
	fmt.Printf("状态码分布: %v\n", statuses)
}
