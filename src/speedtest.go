// speedtest.go
package main

import (
	"bufio"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"daxionglink/protocol" // 根据你的 module 路径进行调整

	"github.com/oschwald/geoip2-golang"
)

// ServerResult 保存单个服务器测速结果
type ServerResult struct {
	Link        string
	Latency     int64   // 毫秒
	ByteSpeed   float64 // 字节/秒
	Msg         string
	ExtractedIP string // 提取的出口IP
}

func ensureURLScheme(raw string) string {
	if raw == "" {
		return raw
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	return "https://" + raw
}

func runCompare(configFile string) {

	config, _ := LoadConfig(configFile)

	// 从配置中获取 tserver（若未设置则默认 server.txt）
	serverFile := config.TServerFile
	if serverFile == "" {
		serverFile = "server.txt"
	}
	// 读取服务器列表
	servers, err := readLines(serverFile)
	if err != nil || len(servers) == 0 {
		fmt.Println("无法读取", serverFile, "或文件为空:", err)
		return
	}

	// 加载配置
	basePort := config.Port
	testURL := ensureURLScheme(strings.TrimSpace(config.TestURL))
	downloadLink := strings.TrimSpace(config.DownloadLink)
	// 如果配置了 downloadlink，则使用它进行下载测速
	if downloadLink != "" {
		testURL = ensureURLScheme(downloadLink)
		fmt.Println("🚀 使用下载链接进行测速:", testURL)
	}

	maxConcurrency := config.MaxCon

	// 解析 testURL 得到安全文件名后缀
	u, err := url.Parse(testURL)
	testedSite := "unknown"
	if err == nil {
		testedSite = u.Host
	}
	re := regexp.MustCompile(`[^a-zA-Z0-9_-]`)
	safeTestedSite := re.ReplaceAllString(testedSite, "_")
	dateStr := time.Now().Format("20060102")

	// 生成随机ID
	rand.Seed(time.Now().UnixNano())
	runID := fmt.Sprintf("%06d", rand.Intn(1000000))

	testedFileName := fmt.Sprintf("servertested_%s_%s_%s.txt", runID, safeTestedSite, dateStr)
	resultFileName := fmt.Sprintf("servertested_%s_result.txt", runID)

	// 并发测速管道和结果存储
	ch := make(chan string, len(servers))
	for _, s := range servers {
		ch <- s
	}
	close(ch)

	var wg sync.WaitGroup
	var results []ServerResult
	var mu sync.Mutex

	for i := 0; i < maxConcurrency; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			port := basePort + workerID
			for link := range ch {
				cfg := fmt.Sprintf("config_%d.json", workerID)
				if err := protocol.GenerateXrayConfig(link, port, cfg); err != nil {
					mu.Lock()
					results = append(results, ServerResult{Link: link, Latency: 0, ByteSpeed: 0, Msg: fmt.Sprintf("生成配置失败: %v", err)})
					mu.Unlock()
					continue
				}

				xrayExe := "xray"
				if runtime.GOOS == "windows" {
					xrayExe = "./xray.exe"
				}
				cmd := exec.Command(xrayExe, "run", "-c", cfg)
				hideCommandWindow(cmd)
				stdout, _ := cmd.StdoutPipe()
				stderr, _ := cmd.StderrPipe()
				if err := cmd.Start(); err != nil {
					mu.Lock()
					results = append(results, ServerResult{Link: link, Latency: 0, ByteSpeed: 0, Msg: fmt.Sprintf("Xray 启动失败: %v", err)})
					mu.Unlock()
					continue
				}

				go io.Copy(os.Stdout, stdout)
				go io.Copy(os.Stderr, stderr)
				time.Sleep(800 * time.Millisecond)

				// 使用新的 MeasureAdvanced 进行测速
				var speed float64
				var ip string
				var speedErr error
				if downloadLink != "" {
					speed, ip, speedErr = protocol.MeasureDownloadSpeed(port, testURL, 10*time.Second)
				} else {
					speed, ip, speedErr = protocol.MeasureAdvanced(port, testURL)
				}
				_ = cmd.Process.Kill()
				_ = cmd.Wait()

				mu.Lock()
				if speedErr != nil {
					results = append(results, ServerResult{Link: link, Latency: 0, ByteSpeed: 0, Msg: fmt.Sprintf("测速失败: %v", speedErr)})
				} else {
					results = append(results, ServerResult{
						Link:        link,
						Latency:     0,     // 不再重点关注延迟
						ByteSpeed:   speed, // 加权速度
						ExtractedIP: ip,
						Msg:         "OK",
					})
				}
				mu.Unlock()

				os.Remove(cfg)
			}
		}(i)
	}
	wg.Wait()

	// 按速度排序 (统一按 ByteSpeed 降序)
	sort.Slice(results, func(i, j int) bool {
		if results[i].ByteSpeed == 0 {
			return false
		}
		if results[j].ByteSpeed == 0 {
			return true
		}
		return results[i].ByteSpeed > results[j].ByteSpeed
	})

	// 写入 <safe>_YYYYMMDD.txt
	f1, err := os.Create(testedFileName)
	if err != nil {
		fmt.Println("无法创建", testedFileName, ":", err)
		return
	}
	w1 := bufio.NewWriter(f1)
	for _, r := range results {
		// 只要有速度就视为成功
		if r.ByteSpeed > 0 {
			w1.WriteString(r.Link + "\n")
		}
	}
	w1.Flush()
	f1.Close()

	// 复制到 servertested.txt
	fSrc, _ := os.Open(testedFileName)
	defer fSrc.Close()
	fDst, err := os.Create("servertested.txt")
	if err != nil {
		fmt.Println("无法创建 servertested.txt:", err)
		return
	}
	defer fDst.Close()
	io.Copy(fDst, fSrc)

	// 打开 GeoIP 数据库
	exeDir := "."
	ex, err := os.Executable()
	if err != nil {
		fmt.Println("获取可执行文件路径失败:", err)
	} else {
		exeDir = filepath.Dir(ex)
	}
	dbPath := filepath.Join(exeDir, "GeoLite2-City.mmdb")
	db, err := geoip2.Open(dbPath)
	if err != nil {
		fmt.Println("无法打开 GeoIP 数据库:", err)
		db = nil
	} else {
		defer db.Close()
	}
	geoipDB, geoipPath, geoipErr := loadGeoIPDB(defaultGeoPaths(exeDir, "geoip.dat"))
	if geoipErr != nil {
		fmt.Println("未找到 geoip.dat，跳过 geoip 标注")
	} else {
		fmt.Println("✅ 使用 geoip.dat:", geoipPath)
	}
	geositeDB, geositePath, geositeErr := loadGeoSiteDB(defaultGeoPaths(exeDir, "geosite.dat"), defaultGeoSiteTags)
	if geositeErr != nil {
		fmt.Println("未找到 geosite.dat，跳过 geosite 标注")
	} else {
		fmt.Println("✅ 使用 geosite.dat:", geositePath)
	}

	// 写入 result文件
	f2, err := os.Create(resultFileName)
	if err != nil {
		fmt.Println("无法创建 result文件:", err)
		return
	}
	w2 := bufio.NewWriter(f2)
	now := time.Now().Format("2006-01-02 15:04:05")
	for _, r := range results {
		if r.ByteSpeed <= 0 {
			continue
		}
		country, region := "未知", "未知"
		node, _ := protocol.ParseNode(r.Link)
		addr := ""
		sni := ""
		host := ""
		if node != nil {
			addr = strings.TrimSpace(node.Address)
			sni = strings.TrimSpace(node.SNI)
			host = strings.TrimSpace(node.Host)
		}
		ipStr := strings.TrimSpace(r.ExtractedIP)
		if ipStr == "" {
			ipStr = addr
		}
		ipStr = strings.Trim(ipStr, "[]")
		if ipStr == "" {
			if u2, err := url.Parse(r.Link); err == nil {
				ipStr = strings.Trim(u2.Host, "[]")
			}
		}
		if db != nil {
			if ip := net.ParseIP(ipStr); ip != nil {
				if rec, err := db.City(ip); err == nil {
					if n, ok := rec.Country.Names["en"]; ok {
						country = n
					}
					if len(rec.Subdivisions) > 0 {
						if n, ok := rec.Subdivisions[0].Names["en"]; ok {
							region = n
						}
					}
				}
			}
		}
		geoipCode := "unknown"
		geositeTag := "unknown"
		if geoipDB != nil {
			if ip := net.ParseIP(ipStr); ip != nil {
				if code := geoipDB.match(ip); code != "" {
					geoipCode = code
				}
			}
		}
		if geositeDB != nil {
			domain := ""
			if sni != "" {
				domain = sni
			} else if host != "" {
				domain = host
			} else {
				domain = addr
			}
			domain = strings.Trim(domain, "[]")
			if domain != "" && net.ParseIP(domain) == nil {
				if tag := geositeDB.match(domain); tag != "" {
					geositeTag = tag
				}
			}
		}

		speedStr := ""
		if r.ByteSpeed > 1024*1024 {
			speedStr = fmt.Sprintf("%.2f MB/s", r.ByteSpeed/(1024*1024))
		} else {
			speedStr = fmt.Sprintf("%.2f KB/s", r.ByteSpeed/1024)
		}

		line := fmt.Sprintf("[%s] link=%s speed=%s ip=%s msg=%s country=%s region=%s geoip=%s geosite=%s\n",
			now, r.Link, speedStr, r.ExtractedIP, r.Msg, country, region, geoipCode, geositeTag)
		w2.WriteString(line)
	}
	w2.Flush()
	f2.Close()

	fmt.Println("测速完成，结果已写入", testedFileName, "和", resultFileName)

	// 更新配置文件中的 qserver 字段
	if err := updateQServer(configFile, testedFileName); err != nil {
		fmt.Println("更新配置文件失败:", err)
	} else {
		fmt.Println("✅ 已将 qserver 更新为", testedFileName, "，写入", configFile)
	}
}

// updateQServer 读取并修改指定 configFile 中的 qserver 值
func updateQServer(configFile, newQServer string) error {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "qserver") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				lines[i] = parts[0] + "=" + newQServer
			}
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, "qserver="+newQServer)
	}
	out := strings.Join(lines, "\n")
	return os.WriteFile(configFile, []byte(out), 0644)
}
