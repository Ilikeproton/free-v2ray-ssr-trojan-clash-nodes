package protocol

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// parseShadowsocks 解析 ss://BASE64(method:password)@address:port[#remark]
func parseShadowsocks(link string) (*Node, error) {
	raw := strings.TrimPrefix(link, "ss://")
	parts := strings.SplitN(raw, "@", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("ss 链接格式错误")
	}
	encodedUser := parts[0]
	serverPart := parts[1]

	decodedUser, err := base64.StdEncoding.DecodeString(encodedUser)
	if err != nil {
		return nil, fmt.Errorf("ss Base64 解码失败: %v", err)
	}
	userInfo := string(decodedUser)
	userParts := strings.SplitN(userInfo, ":", 2)
	if len(userParts) != 2 {
		return nil, fmt.Errorf("ss 用户信息格式错误")
	}
	method := userParts[0]
	password := userParts[1]

	serverPart = strings.SplitN(serverPart, "#", 2)[0]
	spParts := strings.SplitN(serverPart, ":", 2)
	if len(spParts) != 2 {
		return nil, fmt.Errorf("ss 链接错误: host:port 缺失")
	}
	address := spParts[0]
	port := toInt(spParts[1])

	node := &Node{
		Protocol: "shadowsocks",
		Address:  address,
		Port:     port,
		RawQueryMap: map[string]string{
			"method":   method,
			"password": password,
		},
	}
	return node, nil
}

// buildOutboundShadowsocks 根据 Node 生成 Shadowsocks outbound
func buildOutboundShadowsocks(n *Node) map[string]interface{} {
	method := n.RawQueryMap["method"]
	password := n.RawQueryMap["password"]
	outbound := map[string]interface{}{
		"protocol": "shadowsocks",
		"settings": map[string]interface{}{
			"servers": []map[string]interface{}{
				{
					"address":  n.Address,
					"port":     n.Port,
					"method":   method,
					"password": password,
					"ota":      false,
				},
			},
		},
		"streamSettings": map[string]interface{}{},
	}
	return outbound
}
