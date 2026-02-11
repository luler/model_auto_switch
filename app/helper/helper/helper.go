package helper

import (
	"time"
)

// 格式化日期时间（使用全局设置的时区）
func LocalTimeFormat(t time.Time) string {
	return t.In(time.Local).Format("2006-01-02 15:04:05")
}

// 过滤map[string]interface{}类型的数据
func FilterMap(data map[string]interface{}, fields []string) map[string]interface{} {
	//参数过滤
	if len(fields) == 0 {
		return data
	}

	result := make(map[string]interface{})
	for _, field := range fields {
		if value, exists := data[field]; exists {
			result[field] = value
		}
	}
	return result
}

// 合并多个map[string]interface{}
func MergeMaps(maps ...map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	for _, m := range maps {
		for k, v := range m {
			result[k] = v
		}
	}
	return result
}
