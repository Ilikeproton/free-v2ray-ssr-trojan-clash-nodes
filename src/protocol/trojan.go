package protocol

import (
    "fmt"
    "strings"
)

// parseTrojan 解析 trojan://PASSWORD@HOST:PORT?query#remark
func parseTrojan(link string) (*Node, error) {
    raw := strings.TrimPrefix(link, "trojan://")
    // 去掉 #remark
    parts := strings.SplitN(raw, "#", 2)
    raw = parts[0]

    // 分割出 query
    parts = strings.SplitN(raw, "?", 2)
    basePart := parts[0]
    queryStr := ""
    if len(parts) > 1 {
        queryStr = parts[1]
    }

    // basePart 应当形如 "PASSWORD@HOST:PORT"
    userHost := strings.SplitN(basePart, "@", 2)
    if len(userHost) != 2 {
        return nil, fmt.Errorf("trojan 链接格式错误: 缺少 password@host:port")
    }
    password := userHost[0]
    hostPort := userHost[1]

    hp := strings.SplitN(hostPort, ":", 2)
    if len(hp) != 2 {
        return nil, fmt.Errorf("trojan 链接错误: host:port 缺失")
    }
    address := hp[0]
    portStr := hp[1]

    // 解析 query
    params := parseQuery(queryStr)

    node := &Node{
        Protocol:  "trojan",
        Address:   address,
        Port:      toInt(portStr),
        Password:  password,          // Trojan 密码
        TLS:       params["security"],// 可能是 "tls" 或 "reality"
        SNI:       params["sni"],     // SNI
        FP:        params["fp"],      // 如果将来支持 Trojan+Reality，也可以带 fingerprint
        PBK:       params["pbk"],     // Reality 公钥
        ShortID:   params["sid"],     // Reality shortId
        // 其他想要的参数可以放到 RawQueryMap 里
        RawQueryMap: params,
    }
    return node, nil
}

// buildOutboundTrojan 根据 Node 生成 Trojan outbound
func buildOutboundTrojan(n *Node) map[string]interface{} {
    outbound := map[string]interface{}{
        "protocol": "trojan",
        "settings": map[string]interface{}{
            "servers": []map[string]interface{}{
                {
                    "address":  n.Address,
                    "port":     n.Port,
                    "password": n.Password, // 必须
                },
            },
        },
        "streamSettings": map[string]interface{}{
            // Trojan 通常就是 tcp
            "network": "tcp",
            // 如果是 Trojan + TLS，就写 tls；如果是 Trojan + Reality，就写 reality
            "security": n.TLS,
        },
    }

    // 如果 security = "tls"，则可能需要 tlsSettings
    if n.TLS == "tls" && n.SNI != "" {
        tlsSettings := map[string]interface{}{
            "serverName": n.SNI,
        }
        // 如果 query 中带 alpn=h2 或 h2,http/1.1
        if alpn := n.RawQueryMap["alpn"]; alpn != "" {
            // 例如 alpn=h2
            tlsSettings["alpn"] = []string{alpn}
        }
        // 也可以加 "allowInsecure": true/false 看需求
        // if n.RawQueryMap["allowInsecure"] == "1" {
        //     tlsSettings["allowInsecure"] = true
        // }
        outbound["streamSettings"].(map[string]interface{})["tlsSettings"] = tlsSettings
    }

    // 如果 security = "reality"，则需要 realitySettings
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
