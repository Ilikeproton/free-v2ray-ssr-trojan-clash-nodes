package protocol

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// GenerateXrayConfig builds a minimal Xray config for one outbound link.
func GenerateXrayConfig(link string, localPort int, configPath string) error {
	var node *Node
	var err error

	// 1) Parse link.
	switch {
	case strings.HasPrefix(link, "vmess://"):
		node, err = parseVmess(link)
	case strings.HasPrefix(link, "vless://"):
		node, err = parseVless(link)
	case strings.HasPrefix(link, "ss://"):
		node, err = parseShadowsocks(link)
	case strings.HasPrefix(link, "trojan://"):
		node, err = parseTrojan(link)
	case strings.HasPrefix(link, "hy2://"),
		strings.HasPrefix(link, "hysteria2://"),
		strings.HasPrefix(link, "hysteria://"):
		node, err = parseHysteria2(link)
	default:
		return fmt.Errorf("unsupported link protocol: %s", link)
	}
	if err != nil {
		return err
	}

	// 2) Build outbound.
	var outbound map[string]interface{}
	switch node.Protocol {
	case "vmess":
		outbound = buildOutboundVmess(node)
	case "vless":
		outbound = buildOutboundVless(node)
	case "shadowsocks":
		outbound = buildOutboundShadowsocks(node)
	case "trojan":
		outbound = buildOutboundTrojan(node)
	case "hysteria2":
		outbound = buildOutboundHysteria2(node)
	default:
		return fmt.Errorf("unknown protocol: %s", node.Protocol)
	}
	if outbound == nil {
		return fmt.Errorf("generate %s outbound failed", node.Protocol)
	}

	// 3) Inbound: socks.
	inbounds := []map[string]interface{}{
		{
			"port":     localPort,
			"listen":   "0.0.0.0",
			"protocol": "socks",
			"settings": map[string]interface{}{
				"auth": "noauth",
				"udp":  true,
			},
		},
	}

	// 4) Assemble config.
	config := map[string]interface{}{
		"log": map[string]string{
			"loglevel": "warning",
		},
		"inbounds":  inbounds,
		"outbounds": []map[string]interface{}{outbound},
	}

	// 5) Write json.
	return writeJSON(config, configPath)
}

func writeJSON(v interface{}, path string) error {
	data, err := marshalIndent(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func marshalIndent(v interface{}) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}
