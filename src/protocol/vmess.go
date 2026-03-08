package protocol

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// parseVmess 解析 vmess://Base64(JSON) 格式
func parseVmess(link string) (*Node, error) {
	encoded := strings.TrimPrefix(link, "vmess://")
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("vmess Base64 解码失败: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("vmess JSON 解析失败: %v", err)
	}

	node := &Node{
		Protocol: "vmess",
		Address:  getStr(m["add"]),
		Port:     toInt(getStr(m["port"])),
		ID:       getStr(m["id"]),
		AlterID:  toInt(getStr(m["aid"])),
		Net:      getStr(m["net"]),
		TLS:      getStr(m["tls"]),
		SNI:      getStr(m["sni"]),
		Path:     getStr(m["path"]),
		Host:     getStr(m["host"]),
	}
	return node, nil
}

// buildOutboundVmess 根据 Node 生成 VMess outbound
func buildOutboundVmess(n *Node) map[string]interface{} {
	security := "auto"
	outbound := map[string]interface{}{
		"protocol": "vmess",
		"settings": map[string]interface{}{
			"vnext": []map[string]interface{}{
				{
					"address": n.Address,
					"port":    n.Port,
					"users": []map[string]interface{}{
						{
							"id":       n.ID,
							"alterId":  n.AlterID,
							"security": security,
						},
					},
				},
			},
		},
		"streamSettings": map[string]interface{}{
			"network":  n.Net,
			"security": n.TLS,
		},
	}

	// 若使用 TLS + SNI
	if n.TLS == "tls" && n.SNI != "" {
		outbound["streamSettings"].(map[string]interface{})["tlsSettings"] = map[string]interface{}{
			"serverName": n.SNI,
		}
	}
	// 若使用 WebSocket
	if n.Net == "ws" {
		outbound["streamSettings"].(map[string]interface{})["wsSettings"] = map[string]interface{}{
			"path": n.Path,
			"headers": map[string]interface{}{
				"Host": n.Host,
			},
		}
	}
	return outbound
}
