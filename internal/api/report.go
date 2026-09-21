package api

import (
	"encoding/json"
	"strconv"
	"time"

	"agent-platform/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
)

// HandleCreateReportTask 创建定时报告任务(§7)。
func (a *App) HandleCreateReportTask(c *gin.Context) {
	var body struct {
		Name           string   `json:"name"`
		CronExpr       string   `json:"cron_expr"`
		KbIDs          []uint64 `json:"kb_ids"`
		PromptTemplate string   `json:"prompt_template"`
		Notify         struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"notify"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		Fail(c, 400, "请求体错误")
		return
	}
	if body.Name == "" || body.CronExpr == "" {
		Fail(c, 400, "name 与 cron_expr 不能为空")
		return
	}
	schedule, err := cron.ParseStandard(body.CronExpr)
	if err != nil {
		Fail(c, 400, "cron 表达式无效: "+err.Error())
		return
	}
	if body.Notify.Type == "" {
		body.Notify.Type = "console" // 默认控制台渠道,本地演示无需外部接收端
	}
	if body.Notify.Type == "webhook" && body.Notify.URL == "" {
		Fail(c, 400, "webhook 渠道必须配置 url")
		return
	}
	kbJSON, _ := json.Marshal(body.KbIDs)
	notifyJSON, _ := json.Marshal(body.Notify)
	now := time.Now()
	next := schedule.Next(now)

	task := model.ReportTask{
		TenantID:       "default",
		Name:           body.Name,
		CronExpr:       body.CronExpr,
		KbIDs:          kbJSON,
		PromptTemplate: body.PromptTemplate,
		NotifyChannel:  body.Notify.Type,
		NotifyConfig:   notifyJSON,
		Status:         "active",
		NextRunAt:      &next,
	}
	if err := a.DB.WithContext(c.Request.Context()).Create(&task).Error; err != nil {
		Fail(c, 500, "创建报告任务失败")
		return
	}
	OK(c, task)
}

// HandleDeleteReportTask 删除报告任务(含执行记录,事务级联)。
func (a *App) HandleDeleteReportTask(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Fail(c, 400, "任务 ID 无效")
		return
	}
	err = a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("task_id = ?", id).Delete(&model.ReportRun{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.ReportTask{}, id).Error
	})
	if err != nil {
		Fail(c, 500, "删除报告任务失败")
		return
	}
	OK(c, gin.H{"deleted": id})
}

// HandleListReportTasks 报告任务列表(§7)。
func (a *App) HandleListReportTasks(c *gin.Context) {
	var tasks []model.ReportTask
	if err := a.DB.WithContext(c.Request.Context()).Order("id DESC").Find(&tasks).Error; err != nil {
		Fail(c, 500, "查询报告任务失败")
		return
	}
	OK(c, tasks)
}

// HandleListReportRuns 报告执行记录列表(§7)。
func (a *App) HandleListReportRuns(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Fail(c, 400, "任务 ID 无效")
		return
	}
	var runs []model.ReportRun
	if err := a.DB.WithContext(c.Request.Context()).
		Where("task_id = ?", id).Order("id DESC").Limit(20).Find(&runs).Error; err != nil {
		Fail(c, 500, "查询执行记录失败")
		return
	}
	OK(c, runs)
}
