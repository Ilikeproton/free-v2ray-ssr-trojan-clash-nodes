package protocol

import (
	"fmt"
	"strings"
)

// Node stores normalized proxy link fields.
type Node struct {
	Protocol      string
	Address       string
	Port          int
	ID            string
	Net           string // tcp / ws / grpc / ...
	TLS           string // tls / reality / ...
	SNI           string
	Path          string
	Host          string
	Flow          string
	FP            string
	PBK           string
	ShortID       string
	Encryption    string
	AlterID       int
	ServiceName   string // gRPC serviceName
	Password      string // trojan/hysteria auth
	AllowInsecure bool
	ALPN          []string
	Congestion    string
	Up            string
	Down          string
	Obfs          string
	ObfsPassword  string
	RawQueryMap   map[string]string
}

// getStr converts interface{} to string.
func getStr(v interface{}) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

// toInt converts string to int.
func toInt(s string) int {
	var i int
	fmt.Sscanf(s, "%d", &i)
	return i
}

// parseQuery parses query string like "k1=v1&k2=v2".
func parseQuery(queryStr string) map[string]string {
	m := make(map[string]string)
	pairs := strings.Split(queryStr, "&")
	for _, pair := range pairs {
		if pair == "" {
			continue
		}
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) == 2 {
			m[kv[0]] = kv[1]
		} else {
			m[kv[0]] = ""
		}
	}
	return m
}

// ParseNode exposes protocol parsing for external callers (e.g., result annotation).
func ParseNode(link string) (*Node, error) {
	switch {
	case strings.HasPrefix(link, "vmess://"):
		return parseVmess(link)
	case strings.HasPrefix(link, "vless://"):
		return parseVless(link)
	case strings.HasPrefix(link, "ss://"):
		return parseShadowsocks(link)
	case strings.HasPrefix(link, "trojan://"):
		return parseTrojan(link)
	case strings.HasPrefix(link, "hy2://"),
		strings.HasPrefix(link, "hysteria2://"),
		strings.HasPrefix(link, "hysteria://"):
		return parseHysteria2(link)
	default:
		return nil, fmt.Errorf("unsupported link protocol: %s", link)
	}
}
