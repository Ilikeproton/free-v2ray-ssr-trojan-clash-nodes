// config.go
package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// AppConfig 存储应用的所有配置项
type AppConfig struct {
	Port         int    // 基础端口
	QServerFile  string // 快速连接使用的服务器列表文件
	TServerFile  string // 测速使用的服务器列表文件
	TestURL      string // 测速目标 URL
	DownloadLink string // 下载测速链接
	Core         string // 核心程序 (xray 或 sing-box)
	MaxCon       int    // 最大并发连接数
}

// LoadConfig 从指定的 configFile 加载配置，若文件不存在则使用内置默认值
func LoadConfig(configFile string) (*AppConfig, error) {
	// 默认值
	config := &AppConfig{
		Port:         11000,
		QServerFile:  "servertested.txt",
		TServerFile:  "server.txt",
		TestURL:      "https://fast.com",
		DownloadLink: "",
		Core:         "xray",
		MaxCon:       5,
	}

	file, err := os.Open(configFile)
	if err != nil {
		// 若配置文件不存在，则直接返回默认配置
		return config, nil
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) < 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		switch key {
		case "port":
			if p, err := strconv.Atoi(value); err == nil && p > 0 {
				config.Port = p
			}
		case "qserver":
			config.QServerFile = value
		case "tserver":
			config.TServerFile = value
		case "testurl":
			config.TestURL = value
		case "downloadlink":
			config.DownloadLink = value
		case "core":
			if value == "xray" || value == "sing-box" {
				config.Core = value
			} else {
				fmt.Println("⚠️ 无效的 core，默认为 xray")
			}
		case "connection", "maxcon":
			if m, err := strconv.Atoi(value); err == nil && m > 0 {
				config.MaxCon = m
			} else {
				fmt.Println("⚠️ 无效的连接数，使用默认值")
			}
		}
	}

	return config, scanner.Err()
}
