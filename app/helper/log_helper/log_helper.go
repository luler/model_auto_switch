package log_helper

import (
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"
	"gopkg.in/natefinch/lumberjack.v2"
)

var logHelper *logrus.Logger

// logFormatter 自定义日志格式：时间 + 级别 + 消息
// 级别在括号内补齐到固定宽度，保证消息起始列对齐，便于扫读。
// Emoji 由各调用点自行加入消息（按业务语义区分），此处不处理。
type logFormatter struct {
	TimestampFormat string
}

func (f *logFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	ts := f.TimestampFormat
	if ts == "" {
		ts = "2006-01-02 15:04:05.000"
	}

	// 级别补齐到 7 位（最长为 WARNING），保证 [LEVEL] 段宽度一致
	levelTag := fmt.Sprintf("%-7s", strings.ToUpper(entry.Level.String()))

	// 拼接附加字段（WithField），避免信息丢失
	var fieldStr string
	if len(entry.Data) > 0 {
		parts := make([]string, 0, len(entry.Data))
		for k, v := range entry.Data {
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
		fieldStr = " " + strings.Join(parts, " ")
	}

	return []byte(fmt.Sprintf("%s [%s] %s%s\n",
		entry.Time.Format(ts), levelTag, entry.Message, fieldStr)), nil
}

// 初始化日志助手
func InitlogHelper() {
	logHelper = logrus.New()
	// 设置日志级别为 Info
	logHelper.SetLevel(logrus.TraceLevel)
	//设置日志格式：时间 + 级别 + 消息
	logHelper.SetFormatter(&logFormatter{
		TimestampFormat: "2006-01-02 15:04:05.000",
	})
	// 创建一个新的 lumberjack.Logger 实例
	logFilePath := "./runtime/logs/app.log"
	hook := &lumberjack.Logger{
		Filename:   logFilePath,
		MaxSize:    50,  // 单位：MB
		MaxAge:     365, // 保留时间：天
		MaxBackups: 100, // 最大备份数量
	}

	// 设置日志输出到 hook
	logHelper.SetOutput(hook)
}

// 写日志
func Info(args ...interface{}) {
	logHelper.Info(args...)
}
func Error(args ...interface{}) {
	logHelper.Error(args...)
}
func Warning(args ...interface{}) {
	logHelper.Warning(args...)
}
func Debug(args ...interface{}) {
	logHelper.Debug(args...)
}
func Fatal(args ...interface{}) {
	logHelper.Fatal(args...)
}
