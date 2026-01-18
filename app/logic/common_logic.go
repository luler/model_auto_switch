package logic

// 限制字符长度，超过显示...超过显示
func TruncateWithEllipsis(s string, maxLength int) string {
	if maxLength < 3 {
		return s
	}

	runes := []rune(s)
	if len(runes) <= maxLength {
		return s
	}

	return string(runes[:maxLength-3]) + "..."
}
