// Package store 封装 MySQL(GORM)与 Redis(go-redis)的初始化。
package store

import (
	"fmt"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/model"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// NewMySQL 连接 MySQL:先以无库名 DSN 建库(agent),再连目标库并 AutoMigrate 9 张表。
// 为什么分两步:建库需在无库连接上执行 CREATE DATABASE IF NOT EXISTS,再切换。
func NewMySQL(cfg *config.Config) (*gorm.DB, error) {
	// 1) 建库(若不存在)
	serverDSN := cfg.Env.DB.DSN("")
	db, err := gorm.Open(mysql.Open(serverDSN), gormConfig())
	if err != nil {
		return nil, fmt.Errorf("连接 MySQL 服务器: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("获取 MySQL 底层连接: %w", err)
	}
	createSQL := fmt.Sprintf(
		"CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci",
		cfg.Env.DB.DBName,
	)
	if err := db.Exec(createSQL).Error; err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("创建数据库 %s: %w", cfg.Env.DB.DBName, err)
	}
	sqlDB.Close() // 无库连接仅用于建库,用完即关

	// 2) 连目标库 + 连接池
	db, err = gorm.Open(mysql.Open(cfg.Env.DB.DSN(cfg.Env.DB.DBName)), gormConfig())
	if err != nil {
		return nil, fmt.Errorf("连接数据库 %s: %w", cfg.Env.DB.DBName, err)
	}
	sqlDB, err = db.DB()
	if err != nil {
		return nil, fmt.Errorf("获取数据库连接池: %w", err)
	}
	sqlDB.SetMaxOpenConns(cfg.Store.MySQLMaxOpen)
	sqlDB.SetMaxIdleConns(cfg.Store.MySQLMaxIdle)
	sqlDB.SetConnMaxLifetime(time.Hour)

	// 3) AutoMigrate 9 张表
	if err := db.AutoMigrate(
		&model.User{},
		&model.KbDocument{},
		&model.KbChunk{},
		&model.Session{},
		&model.Message{},
		&model.ReportTask{},
		&model.ReportRun{},
		&model.UsageStat{},
		&model.ToolEvent{},
	); err != nil {
		return nil, fmt.Errorf("AutoMigrate 建表失败: %w", err)
	}

	// 4) 中文全文检索依赖 ngram 分词器(默认 FULLTEXT parser 对中文几乎不分词)。
	//    AutoMigrate 已建默认索引 idx_content;此处追加 ngram 索引,幂等。
	if err := ensureNgramIndex(db, cfg.Env.DB.DBName); err != nil {
		return nil, err
	}
	return db, nil
}

// ensureNgramIndex 为 kb_chunks.content 追加 ngram 全文索引(仅建一次)。
func ensureNgramIndex(db *gorm.DB, dbName string) error {
	var cnt int64
	if err := db.Raw(
		"SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema=? AND table_name='kb_chunks' AND index_name='idx_content_ngram'",
		dbName,
	).Scan(&cnt).Error; err != nil {
		return fmt.Errorf("检查 ngram 索引: %w", err)
	}
	if cnt > 0 {
		return nil
	}
	if err := db.Exec("ALTER TABLE kb_chunks ADD FULLTEXT INDEX idx_content_ngram (content) WITH PARSER ngram").Error; err != nil {
		return fmt.Errorf("创建 ngram 全文索引: %w", err)
	}
	return nil
}

// gormConfig 统一 GORM 日志级别(仅打印慢 SQL 与错误,避免刷屏)。
func gormConfig() *gorm.Config {
	return &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	}
}
