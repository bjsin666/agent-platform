package api

import (
	"io"
	"strconv"

	"agent-platform/internal/model"

	"github.com/gin-gonic/gin"
)

// maxUploadBytes 单文件上传上限(10MB)。
const maxUploadBytes = 10 << 20

// HandleUploadDocument 上传文档(multipart field: file)。入库异步,返回 doc_id + status(§7)。
func (a *App) HandleUploadDocument(c *gin.Context) {
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		Fail(c, 400, "缺少 file 字段")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxUploadBytes))
	if err != nil {
		Fail(c, 500, "读取文件失败")
		return
	}
	if len(data) == 0 {
		Fail(c, 400, "文件内容为空")
		return
	}

	doc := model.KbDocument{
		TenantID: "default",
		Title:    header.Filename,
		Source:   header.Filename,
		Status:   "processing",
	}
	if err := a.DB.WithContext(c.Request.Context()).Create(&doc).Error; err != nil {
		Fail(c, 500, "创建文档记录失败")
		return
	}
	// 异步入库(worker 处理完改 status=ready)
	a.KB.Submit(doc.ID, string(data))

	OK(c, gin.H{"doc_id": doc.ID, "status": "processing"})
}

// HandleListDocuments 文档列表(§7)。
func (a *App) HandleListDocuments(c *gin.Context) {
	var docs []model.KbDocument
	if err := a.DB.WithContext(c.Request.Context()).Order("id DESC").Find(&docs).Error; err != nil {
		Fail(c, 500, "查询文档失败")
		return
	}
	OK(c, docs)
}

// HandleDeleteDocument 删除文档并级联删除 chunks(§7/§8.5)。
func (a *App) HandleDeleteDocument(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Fail(c, 400, "文档 ID 无效")
		return
	}
	if err := a.KB.Delete(c.Request.Context(), id); err != nil {
		Fail(c, 500, "删除文档失败")
		return
	}
	OK(c, gin.H{"deleted": id})
}
