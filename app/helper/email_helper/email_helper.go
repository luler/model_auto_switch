package email_helper

import (
	"crypto/tls"
	"fmt"
	"net/smtp"
	"os"
	"strconv"
	"strings"
)

// EmailConfig SMTP 配置
type EmailConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	FromName string
}

// EmailMessage 邮件内容
type EmailMessage struct {
	To      []string // 收件人列表
	Cc      []string // 抄送列表
	Subject string   // 邮件主题
	Body    string   // 邮件正文
	IsHTML  bool     // 是否为HTML格式
}

// EmailResult 发送结果
type EmailResult struct {
	Success bool
	Error   string
}

// GetDefaultConfig 从环境变量获取默认配置
func GetDefaultConfig() EmailConfig {
	port, _ := strconv.Atoi(os.Getenv("SMTP_PORT"))
	if port == 0 {
		port = 587
	}
	return EmailConfig{
		Host:     os.Getenv("SMTP_HOST"),
		Port:     port,
		Username: os.Getenv("SMTP_USERNAME"),
		Password: os.Getenv("SMTP_PASSWORD"),
		From:     os.Getenv("SMTP_FROM"),
		FromName: os.Getenv("SMTP_FROM_NAME"),
	}
}

// SendEmail 发送邮件
func SendEmail(config EmailConfig, message EmailMessage) EmailResult {
	if config.Host == "" {
		return EmailResult{Success: false, Error: "SMTP Host 未配置"}
	}
	if len(message.To) == 0 {
		return EmailResult{Success: false, Error: "收件人不能为空"}
	}

	// 构建邮件头
	headers := make(map[string]string)
	if config.FromName != "" {
		headers["From"] = fmt.Sprintf("%s <%s>", config.FromName, config.From)
	} else {
		headers["From"] = config.From
	}
	headers["To"] = strings.Join(message.To, ",")
	if len(message.Cc) > 0 {
		headers["Cc"] = strings.Join(message.Cc, ",")
	}
	headers["Subject"] = message.Subject
	headers["MIME-Version"] = "1.0"
	if message.IsHTML {
		headers["Content-Type"] = "text/html; charset=UTF-8"
	} else {
		headers["Content-Type"] = "text/plain; charset=UTF-8"
	}

	// 构建邮件内容
	var msgBuilder strings.Builder
	for k, v := range headers {
		msgBuilder.WriteString(fmt.Sprintf("%s: %s\r\n", k, v))
	}
	msgBuilder.WriteString("\r\n")
	msgBuilder.WriteString(message.Body)

	// 合并所有收件人
	allRecipients := append(message.To, message.Cc...)

	// 连接 SMTP 服务器
	addr := fmt.Sprintf("%s:%d", config.Host, config.Port)

	// 认证
	auth := smtp.PlainAuth("", config.Username, config.Password, config.Host)

	// 根据端口选择连接方式
	var err error
	if config.Port == 465 {
		// SSL 连接
		err = sendMailWithSSL(addr, auth, config.From, allRecipients, []byte(msgBuilder.String()))
	} else {
		// TLS 或普通连接 (587, 25)
		err = smtp.SendMail(addr, auth, config.From, allRecipients, []byte(msgBuilder.String()))
	}

	if err != nil {
		return EmailResult{Success: false, Error: err.Error()}
	}

	return EmailResult{Success: true, Error: ""}
}

// sendMailWithSSL 使用 SSL 发送邮件 (端口465)
func sendMailWithSSL(addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
	host := strings.Split(addr, ":")[0]

	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         host,
	}

	conn, err := tls.Dial("tcp", addr, tlsConfig)
	if err != nil {
		return err
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer client.Close()

	if auth != nil {
		if err = client.Auth(auth); err != nil {
			return err
		}
	}

	if err = client.Mail(from); err != nil {
		return err
	}

	for _, addr := range to {
		if err = client.Rcpt(addr); err != nil {
			return err
		}
	}

	w, err := client.Data()
	if err != nil {
		return err
	}

	_, err = w.Write(msg)
	if err != nil {
		return err
	}

	err = w.Close()
	if err != nil {
		return err
	}

	return client.Quit()
}

// SendEmailWithDefaultConfig 使用默认配置发送邮件
func SendEmailWithDefaultConfig(message EmailMessage) EmailResult {
	config := GetDefaultConfig()
	return SendEmail(config, message)
}
