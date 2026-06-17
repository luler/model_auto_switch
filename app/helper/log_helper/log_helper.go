package log_helper

import (
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"
	"gopkg.in/natefinch/lumberjack.v2"
)

var logHelper *logrus.Logger

// emojiFormatter 自定义日志格式：时间 + 级别 + Emoji + 消息
// 用 Emoji 直观区分成功(✅) / 告警(⚠️) / 失败(❌) 等，便于快速扫读
type emojiFormatter struct {
	TimestampFormat string
}

func (f *emojiFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	ts := f.TimestampFormat
	if ts == "" {
		ts = "2006-01-02 15:04:05.000"
	}

	// 级别填充到固定宽度，保证各行列对齐
	levelTag := fmt.Sprintf("%-5s", strings.ToUpper(entry.Level.String()))
	emoji := levelEmoji(entry.Level)

	// 拼接附加字段（WithField），避免信息丢失
	var fieldStr string
	if len(entry.Data) > 0 {
		parts := make([]string, 0, len(entry.Data))
		for k, v := range entry.Data {
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
		fieldStr = " " + strings.Join(parts, " ")
	}

	return []byte(fmt.Sprintf("%s [%s] %s %s%s\n",
		entry.Time.Format(ts), levelTag, emoji, entry.Message, fieldStr)), nil
}

// levelEmoji 按日志级别返回对应的 Emoji
func levelEmoji(level logrus.Level) string {
	switch level {
	case logrus.PanicLevel, logrus.FatalLevel:
		return "💀"
	case logrus.ErrorLevel:
		return "❌"
	case logrus.WarnLevel:
		return "⚠️"
	case logrus.InfoLevel:
		return "✅"
	case logrus.DebugLevel:
		return "🔍"
	case logrus.TraceLevel:
		return "🔬"
	default:
		return "•"
	}
}

// 初始化日志助手
func InitlogHelper() {
	logHelper = logrus.New()
	// 设置日志级别为 Info
	logHelper.SetLevel(logrus.TraceLevel)
	//设置日志格式：时间 + 级别 + Emoji + 消息
	logHelper.SetFormatter(&emojiFormatter{
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
