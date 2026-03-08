package protocol

import (
	"fmt"
	"strings"
)

// parseVless 解析 vless://<uuid>@<address>:<port>?query#remark
func parseVless(link string) (*Node, error) {
	raw := strings.TrimPrefix(link, "vless://")
	parts := strings.SplitN(raw, "#", 2)
	raw = parts[0]
	parts = strings.SplitN(raw, "?", 2)
	basePart := parts[0]
	queryStr := ""
	if len(parts) > 1 {
		queryStr = parts[1]
	}

	userHost := strings.SplitN(basePart, "@", 2)
	if len(userHost) != 2 {
		return nil, fmt.Errorf("vless 链接格式错误")
	}
	uuid := userHost[0]
	hostPort := userHost[1]

	hp := strings.SplitN(hostPort, ":", 2)
	if len(hp) != 2 {
		return nil, fmt.Errorf("vless 链接错误: host:port 缺失")
	}
	address := hp[0]
	portStr := hp[1]

	params := parseQuery(queryStr)

	node := &Node{
		Protocol:   "vless",
		Address:    address,
		Port:       toInt(portStr),
		ID:         uuid,
		Net:        params["type"],
		TLS:        params["security"], // 可能是 tls / reality
		SNI:        params["sni"],
		Path:       params["path"],
		Host:       params["host"],
		Flow:       params["flow"],  // xtls-rprx-vision 等
		FP:         params["fp"],    // fingerprint
		PBK:        params["pbk"],   // publicKey
		ShortID:    params["sid"],   // reality shortId
		Encryption: params["encryption"],
		ServiceName: params["serviceName"], // <-- 新增：解析 serviceName
		RawQueryMap: params,
	}
	return node, nil
}

// buildOutboundVless 根据 Node 生成 VLESS outbound
func buildOutboundVless(n *Node) map[string]interface{} {
	// encryption 默认 none
	enc := n.Encryption
	if enc == "" {
		enc = "none"
	}
	outbound := map[string]interface{}{
		"protocol": "vless",
		"settings": map[string]interface{}{
			"vnext": []map[string]interface{}{
				{
					"address": n.Address,
					"port":    n.Port,
					"users": []map[string]interface{}{
						{
							"id":         n.ID,
							"encryption": enc,
							"flow":       n.Flow, // 例如 xtls-rprx-vision
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
	// TLS
	if n.TLS == "tls" && n.SNI != "" {
		outbound["streamSettings"].(map[string]interface{})["tlsSettings"] = map[string]interface{}{
			"serverName": n.SNI,
		}
	}
	// WebSocket
	if n.Net == "ws" {
		outbound["streamSettings"].(map[string]interface{})["wsSettings"] = map[string]interface{}{
			"path": n.Path,
			"headers": map[string]interface{}{
				"Host": n.Host,
			},
		}
	}

	// 3. gRPC
    if strings.ToLower(n.Net) == "grpc" {
        outbound["streamSettings"].(map[string]interface{})["grpcSettings"] = map[string]interface{}{
            "serviceName": n.ServiceName, // 必须有
            // "multiMode": true, // 如果服务器端支持多路复用，可以加上
        }
        // 如果服务器端需要 ALPN = ["h2"]，可以加:
        // outbound["streamSettings"].(map[string]interface{})["tlsSettings"] = map[string]interface{}{
        //     "serverName": n.SNI,
        //     "alpn": []string{"h2"},
        // }
    }


	// Reality
	if n.TLS == "reality" {
		shortId := n.ShortID
		if shortId == "" {
			shortId = "00000000"
		}
		fp := n.FP
		if fp == "" {
			fp = "chrome"
		}
		realitySettings := map[string]interface{}{
			"publicKey":  n.PBK,
			"serverName": n.SNI,
			"shortId":    shortId,
			"fingerprint": fp,
		}
		outbound["streamSettings"].(map[string]interface{})["realitySettings"] = realitySettings
	}
	return outbound
}