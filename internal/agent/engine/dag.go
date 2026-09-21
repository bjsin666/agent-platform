package engine

import (
	"context"
	"fmt"
	"sync"
)

// Node DAG 节点:Deps 为该节点的依赖(先于它执行),Run 为节点逻辑。
type Node struct {
	ID   string
	Deps []string
	Run  func(ctx context.Context) error
}

// DAG 有向无环图工作流。
type DAG struct {
	Nodes []Node
}

// DAGRun 执行 DAG:入度调度 + worker 池(concurrency 个)并发执行。
// 任一节点失败 -> cancel 上下文,未开始的节点跳过执行;存在环或依赖不可达时返回错误。
//
// 实现要点:就绪队列与执行计数放在同一把互斥锁 + sync.Cond 下维护,
// "取任务"与 inflight++ 原子完成,避免监控在 worker 取任务的间隙误判死锁
// (经典竞态:worker 已从队列取出任务、尚未计入在途时,若监控发现"无在途无就绪"
// 就会错误地判为环)。
func DAGRun(ctx context.Context, dag *DAG, concurrency int) error {
	if concurrency <= 0 {
		concurrency = 1
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// 索引与依赖结构
	index := make(map[string]*Node, len(dag.Nodes))
	children := map[string][]string{}
	remains := map[string]int{} // 未完成依赖数

	// 第一遍建索引:Deps 可能引用后声明的节点,依赖校验必须等全量索引就绪
	for i := range dag.Nodes {
		n := &dag.Nodes[i]
		if _, dup := index[n.ID]; dup {
			return fmt.Errorf("DAG 节点 ID 重复: %s", n.ID)
		}
		index[n.ID] = n
	}
	// 第二遍校验依赖并建立子关系
	for i := range dag.Nodes {
		n := &dag.Nodes[i]
		remains[n.ID] = len(n.Deps)
		for _, dep := range n.Deps {
			if dep == n.ID {
				return fmt.Errorf("DAG 节点自依赖: %s", n.ID)
			}
			if _, ok := index[dep]; !ok {
				return fmt.Errorf("DAG 节点 %s 依赖不存在的节点 %s", n.ID, dep)
			}
			children[dep] = append(children[dep], n.ID)
		}
	}

	var (
		mu        sync.Mutex
		cond      = sync.NewCond(&mu)
		queue     []string // 就绪队列
		inflight  int      // 正在执行的节点数
		done      int      // 已完成节点数(含跳过)
		remaining int      // 未完成节点数(含在途/排队)
		nodeErr   error
		finished  bool
		wg        sync.WaitGroup
	)

	// 种子就绪节点(入度为 0)
	remaining = len(dag.Nodes)
	for id, d := range remains {
		if d == 0 {
			queue = append(queue, id)
		}
	}
	// 启动即无入度 0 节点 -> 环或依赖不可达
	if len(queue) == 0 && remaining > 0 {
		return fmt.Errorf("DAG 存在环或不可达节点:完成 0/%d", len(dag.Nodes))
	}

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				for len(queue) == 0 && !finished && ctx.Err() == nil {
					cond.Wait()
				}
				if len(queue) == 0 || finished {
					mu.Unlock()
					return // 无任务可做(已取消/失败/全部完成)
				}
				id := queue[0]
				queue = queue[1:]
				inflight++
				mu.Unlock()

				// 执行(上下文已取消则跳过,不再跑节点逻辑)
				n := index[id]
				var runErr error
				if ctx.Err() == nil {
					runErr = n.Run(ctx)
				} else {
					runErr = ctx.Err()
				}
				if runErr != nil {
					cancel() // 失败传播:取消在途与后续节点
				}

				mu.Lock()
				inflight--
				done++
				remaining--
				if runErr != nil && nodeErr == nil {
					nodeErr = runErr
				}
				// 成功才解除下游依赖;失败/取消则不再派发
				if nodeErr == nil {
					for _, c := range children[id] {
						remains[c]--
						if remains[c] == 0 {
							queue = append(queue, c)
						}
					}
				}
				// 终止条件判定(在锁内完成,保证状态一致)
				if nodeErr != nil || remaining == 0 {
					finished = true
					cond.Broadcast()
				} else if len(queue) == 0 && inflight == 0 {
					// 队列与在途都空但未完成 -> 环或依赖不可达
					nodeErr = fmt.Errorf("DAG 存在环或不可达节点:完成 %d/%d", done, len(dag.Nodes))
					finished = true
					cond.Broadcast()
				} else if len(queue) > 0 {
					cond.Signal() // 唤醒一个 worker 取新任务
				}
				mu.Unlock()
			}
		}()
	}

	// 主流程:等待结束信号
	mu.Lock()
	for !finished {
		cond.Wait()
	}
	err := nodeErr
	mu.Unlock()

	cancel() // 让仍阻塞在 cond.Wait 的 worker 退出
	wg.Wait()
	return err
}
