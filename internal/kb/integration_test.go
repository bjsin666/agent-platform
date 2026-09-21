package kb

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/embedding"
	"agent-platform/internal/model"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestKBIntegration 端到端集成:真实 MySQL + 真实 embedding 服务,走 Ingest/Search/Delete。
// 默认跳过;设置 KB_TEST_INTEGRATION=1 且 MySQL/embed 服务可用时执行。
func TestKBIntegration(t *testing.T) {
	if os.Getenv("KB_TEST_INTEGRATION") != "1" {
		t.Skip("设置 KB_TEST_INTEGRATION=1 运行知识库集成测试")
	}
	dsn := os.Getenv("MYSQL_DSN")
	if dsn == "" {
		t.Skip("未设置 MYSQL_DSN")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("连接 MySQL: %v", err)
	}
	embedClient := embedding.NewClient("http://127.0.0.1:8001", 32, 10*time.Second, 2)

	svc := NewService(db, embedClient, 5, 800, 50)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := svc.Load(ctx); err != nil {
		t.Fatalf("加载向量缓存: %v", err)
	}

	// 入库一篇示例文档
	content := `# 会员制度
## 会员等级
普通会员可享受 95 折优惠。
## 升级条件
累计消费满 1000 元可升级为黄金会员,享 88 折。`
	doc := model.KbDocument{TenantID: "test", Title: "会员制度", Status: "processing"}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatalf("创建文档: %v", err)
	}
	if err := svc.Ingest(ctx, doc.ID, content); err != nil {
		t.Fatalf("入库: %v", err)
	}

	// 检索
	out, err := svc.Search(ctx, "如何升级为黄金会员", 3)
	if err != nil {
		t.Fatalf("检索: %v", err)
	}
	if !strings.Contains(out, "黄金会员") || !strings.Contains(out, "累计消费满 1000 元") {
		t.Fatalf("检索未命中相关片段: %s", out)
	}

	// 全文
	full, err := svc.GetDocument(ctx, doc.ID)
	if err != nil || !strings.Contains(full, "会员等级") {
		t.Fatalf("GetDocument: %v %q", err, full)
	}

	// 级联删除后 chunks 清空
	if err := svc.Delete(ctx, doc.ID); err != nil {
		t.Fatalf("删除: %v", err)
	}
	var cnt int64
	db.Model(&model.KbChunk{}).Where("doc_id=?", doc.ID).Count(&cnt)
	if cnt != 0 {
		t.Fatalf("删除后 chunks 未清空: %d", cnt)
	}
}
