package protocol

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

func hasHTTPScheme(raw string) bool {
	return strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://")
}

func normalizeURL(raw string) string {
	if raw == "" {
		return raw
	}
	if hasHTTPScheme(raw) {
		return raw
	}
	return "https://" + raw
}

// MeasureSpeed 保留原函数以兼容旧代码（虽然主要逻辑会切换到新函数）
func MeasureSpeed(port int, website string, isDownload bool) (int64, int64, error) {
	// ... 旧逻辑保留或直接调用新逻辑的简化版 ...
	// 为了简单，这里保留原逻辑，但实际上我们会使用 MeasureAdvanced
	website = normalizeURL(website)
	dialer, err := proxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", port), nil, proxy.Direct)
	if err != nil {
		return 0, 0, fmt.Errorf("创建 SOCKS5 dialer 失败: %v", err)
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.Dial(network, addr)
		},
		ResponseHeaderTimeout: 10 * time.Second,
	}
	client := &http.Client{
		Transport: transport,
	}

	start := time.Now()
	resp, err := client.Get(website)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	if isDownload {
		timer := time.AfterFunc(10*time.Second, func() {
			resp.Body.Close()
		})
		defer timer.Stop()
		n, _ := io.Copy(io.Discard, resp.Body)
		return time.Since(start).Milliseconds(), n, nil
	}

	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		return 0, 0, err
	}
	return time.Since(start).Milliseconds(), n, nil
}

// MeasureAdvanced 执行更复杂的测速：主链接 + 4个辅助链接
// 返回: 加权速度(Byte/s), 提取的IP, 错误信息
func MeasureAdvanced(port int, mainURL string) (float64, string, error) {
	mainURL = normalizeURL(mainURL)
	dialer, err := proxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", port), nil, proxy.Direct)
	if err != nil {
		return 0, "", fmt.Errorf("SOCKS5 error: %v", err)
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.Dial(network, addr)
		},
		DisableKeepAlives: true,
	}
	
	// 辅助测试地址
	auxURLs := []string{
		"https://whatismyipaddress.com/",
		"https://www.myip.com/",
		"https://radar.cloudflare.com/ip",
		"https://2ip.io/",
	}

	ipCounts := make(map[string]int)

	// 辅助函数：测试单个 URL
	testOne := func(urlStr string) (float64, string, error) {
		client := &http.Client{
			Transport: transport,
			Timeout:   5 * time.Second, // 5秒超时
		}

		start := time.Now()
		resp, err := client.Get(urlStr)
		if err != nil {
			return 0, "", err
		}
		defer resp.Body.Close()

		// 读取内容（限制大小以防内存溢出，比如 1MB）
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024)) 
		duration := time.Since(start)
		
		// 如果读取出错且不是因为 EOF，算作部分成功但可能影响内容
		// 这里简单处理：只要有字节数就算速度
		n := int64(len(body))
		
		var speed float64
		if duration.Seconds() > 0 {
			speed = float64(n) / duration.Seconds()
		}

		// 提取 IP
		foundIP := extractIP(string(body))
		return speed, foundIP, nil
	}

	// 1. 测试主 URL (60% 权重)
	mainSpeed, _, err := testOne(mainURL)
	if err != nil || mainSpeed == 0 {
		return 0, "", fmt.Errorf("Main URL failed or speed is 0: %v", err)
	}

	// 2. 测试辅助 URLs (40% 权重，即每个 10% 或者 平均值 * 0.4)
	var totalAuxSpeed float64
	successAuxCount := 0

	for _, u := range auxURLs {
		s, ip, e := testOne(u)
		if e == nil {
			totalAuxSpeed += s
			successAuxCount++
			if ip != "" {
				ipCounts[ip]++
			}
		}
	}

	// 计算加权速度
	// 假设主 URL 占 60%，辅助组占 40%。
	// 如果辅助组全部失败，则只看主 URL？
	// 策略：Final = Main * 0.6 + (Avg(Aux)) * 0.4
	
	var avgAuxSpeed float64
	if successAuxCount > 0 {
		avgAuxSpeed = totalAuxSpeed / float64(successAuxCount) // 使用成功测速的平均值
		// 或者应该除以 4？如果除以 4，那么失败的就算 0。这样比较公平。
		// 用户说 "Above 4 test addresses account for 40% weight"。
		// 意味着这部分的总分为 40 分。
		// 所以应该是 (Sum(AuxSpeed) / 4) * 0.4 ?
		// 还是 Avg(AuxSpeed) * 0.4 ? 
		// 假如 4 个都成功，速度都是 1MB/s。Avg = 1MB/s。
		// 假如 1 个成功，速度 1MB/s。Avg = 1MB/s? 还是 0.25 MB/s?
		// 通常测速是测“能力”，所以如果连不上算 0 速比较合理。
		avgAuxSpeed = totalAuxSpeed / 4.0 
	}

	finalSpeed := (mainSpeed * 0.6) + (avgAuxSpeed * 0.4)

	// 确定最终 IP (出现次数最多的)
	bestIP := ""
	maxCount := 0
	for ip, count := range ipCounts {
		if count > maxCount {
			maxCount = count
			bestIP = ip
		}
	}

	// 如果主测速失败且辅助也全部失败，返回错误
	if err != nil && successAuxCount == 0 {
		return 0, "", err
	}

	return finalSpeed, bestIP, nil
}

// MeasureDownloadSpeed samples download throughput for a fixed duration.
// Returns bytes/sec and optional IP (empty when not detected).
func MeasureDownloadSpeed(port int, downloadURL string, sample time.Duration) (float64, string, error) {
	if sample <= 0 {
		sample = 10 * time.Second
	}

	measureOnce := func(urlStr string) (float64, string, error) {
		urlStr = normalizeURL(urlStr)
		dialer, err := proxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", port), nil, proxy.Direct)
		if err != nil {
			return 0, "", fmt.Errorf("SOCKS5 error: %v", err)
		}
		transport := &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			},
			DisableKeepAlives:     true,
			ResponseHeaderTimeout: 10 * time.Second,
		}
		client := &http.Client{
			Transport: transport,
		}

		ctx, cancel := context.WithTimeout(context.Background(), sample)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
		if err != nil {
			return 0, "", err
		}

		resp, err := client.Do(req)
		if err != nil {
			return 0, "", err
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			return 0, "", fmt.Errorf("HTTP %d", resp.StatusCode)
		}

		start := time.Now()
		buf := make([]byte, 32*1024)
		var n int64
		for {
			nr, er := resp.Body.Read(buf)
			if nr > 0 {
				n += int64(nr)
			}
			if er != nil {
				if errors.Is(er, context.DeadlineExceeded) || errors.Is(er, context.Canceled) || errors.Is(er, net.ErrClosed) {
					break
				}
				if er == io.EOF {
					break
				}
				if n > 0 {
					break
				}
				return 0, "", er
			}
		}

		duration := time.Since(start)
		if duration <= 0 || n <= 0 {
			return 0, "", fmt.Errorf("no data downloaded")
		}
		speed := float64(n) / duration.Seconds()
		return speed, "", nil
	}

	if !hasHTTPScheme(downloadURL) {
		if speed, ip, err := measureOnce("https://" + downloadURL); err == nil {
			return speed, ip, nil
		}
		return measureOnce("http://" + downloadURL)
	}

	return measureOnce(downloadURL)
}

// extractIP 从字符串中提取第一个匹配的公网 IPv4
func extractIP(content string) string {
	// 简单的 IPv4 正则
	re := regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	matches := re.FindAllString(content, -1)
	for _, ip := range matches {
		if !isPrivateIP(ip) {
			return ip
		}
	}
	return ""
}

func isPrivateIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return true // Invalid IP considered private/unusable for our purpose
	}
	if ip.IsLoopback() || ip.IsLinkLocalMulticast() || ip.IsLinkLocalUnicast() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 10:
			return true
		case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
			return true
		case ip4[0] == 192 && ip4[1] == 168:
			return true
		}
		return false
	}
	return false
}
