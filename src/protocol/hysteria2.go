package protocol

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func parseHysteria2(link string) (*Node, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, fmt.Errorf("hysteria2 parse failed: %v", err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "hy2" && scheme != "hysteria2" && scheme != "hysteria" {
		return nil, fmt.Errorf("hysteria2 scheme unsupported: %s", u.Scheme)
	}

	address := strings.TrimSpace(u.Hostname())
	if address == "" {
		return nil, fmt.Errorf("hysteria2 link error: host missing")
	}

	portStr := strings.TrimSpace(u.Port())
	if portStr == "" {
		return nil, fmt.Errorf("hysteria2 link error: port missing")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("hysteria2 link error: invalid port")
	}

	params := u.Query()
	rawQueryMap := make(map[string]string, len(params))
	for k, v := range params {
		if len(v) == 0 {
			rawQueryMap[k] = ""
			continue
		}
		rawQueryMap[k] = v[0]
	}

	auth := parseHy2Auth(u, params)
	security := strings.ToLower(strings.TrimSpace(firstNonEmpty(params.Get("security"), "tls")))
	sni := strings.TrimSpace(firstNonEmpty(params.Get("sni"), params.Get("peer"), params.Get("serverName"), params.Get("servername")))
	if sni == "" {
		sni = address
	}

	alpn := parseALPNValues(params["alpn"])
	if len(alpn) == 0 {
		alpn = parseALPNValues(params["alpn[]"])
	}

	obfs := strings.ToLower(strings.TrimSpace(firstNonEmpty(
		params.Get("obfs"),
		params.Get("obfs_type"),
		params.Get("obfs-type"),
	)))
	obfsPassword := strings.TrimSpace(firstNonEmpty(
		params.Get("obfs-password"),
		params.Get("obfs_password"),
		params.Get("obfsParam"),
		params.Get("obfsparam"),
	))

	n := &Node{
		Protocol: "hysteria2",
		Address:  address,
		Port:     port,
		Net:      "hysteria",
		TLS:      security,
		SNI:      sni,
		Password: auth,
		AllowInsecure: parseTruthy(firstNonEmpty(
			params.Get("insecure"),
			params.Get("allowInsecure"),
			params.Get("allow_insecure"),
			params.Get("skip-cert-verify"),
			params.Get("skip_cert_verify"),
		)),
		ALPN:         alpn,
		Congestion:   strings.ToLower(strings.TrimSpace(params.Get("congestion"))),
		Up:           parseHy2Bandwidth(params, "up"),
		Down:         parseHy2Bandwidth(params, "down"),
		Obfs:         obfs,
		ObfsPassword: obfsPassword,
		RawQueryMap:  rawQueryMap,
	}
	return n, nil
}

func buildOutboundHysteria2(n *Node) map[string]interface{} {
	security := strings.ToLower(strings.TrimSpace(n.TLS))
	if security == "" {
		security = "tls"
	}

	hysteriaSettings := map[string]interface{}{
		"version": 2,
	}
	if v := strings.TrimSpace(n.Password); v != "" {
		hysteriaSettings["auth"] = v
	}
	if v := strings.TrimSpace(n.Congestion); v != "" {
		hysteriaSettings["congestion"] = v
	}
	if v := strings.TrimSpace(n.Up); v != "" {
		hysteriaSettings["up"] = v
	}
	if v := strings.TrimSpace(n.Down); v != "" {
		hysteriaSettings["down"] = v
	}

	streamSettings := map[string]interface{}{
		"network":          "hysteria",
		"security":         security,
		"hysteriaSettings": hysteriaSettings,
	}

	if security == "tls" {
		tlsSettings := map[string]interface{}{}
		if v := strings.TrimSpace(n.SNI); v != "" {
			tlsSettings["serverName"] = v
		}
		if n.AllowInsecure {
			tlsSettings["allowInsecure"] = true
		}
		if len(n.ALPN) > 0 {
			tlsSettings["alpn"] = n.ALPN
		}
		if len(tlsSettings) > 0 {
			streamSettings["tlsSettings"] = tlsSettings
		}
	}

	if strings.EqualFold(strings.TrimSpace(n.Obfs), "salamander") && strings.TrimSpace(n.ObfsPassword) != "" {
		streamSettings["finalmask"] = map[string]interface{}{
			"udp": []map[string]interface{}{
				{
					"type": "salamander",
					"settings": map[string]interface{}{
						"password": strings.TrimSpace(n.ObfsPassword),
					},
				},
			},
		}
	}

	outbound := map[string]interface{}{
		"protocol": "hysteria",
		"settings": map[string]interface{}{
			"version": 2,
			"address": n.Address,
			"port":    n.Port,
		},
		"streamSettings": streamSettings,
	}
	return outbound
}

func parseHy2Auth(u *url.URL, q url.Values) string {
	if u.User != nil {
		name := strings.TrimSpace(u.User.Username())
		if pwd, ok := u.User.Password(); ok {
			pwd = strings.TrimSpace(pwd)
			switch {
			case name != "" && pwd != "":
				return name + ":" + pwd
			case name != "":
				return name
			default:
				return pwd
			}
		}
		if name != "" {
			return name
		}
	}
	return strings.TrimSpace(firstNonEmpty(
		q.Get("auth"),
		q.Get("password"),
		q.Get("passwd"),
		q.Get("token"),
	))
}

func parseHy2Bandwidth(q url.Values, key string) string {
	mbpsKeys := []string{
		key + "mbps",
		key + "_mbps",
		key + "-mbps",
	}
	for _, k := range mbpsKeys {
		if v := strings.TrimSpace(q.Get(k)); v != "" {
			return normalizeBandwidth(v, true)
		}
	}
	if v := strings.TrimSpace(q.Get(key)); v != "" {
		return normalizeBandwidth(v, false)
	}
	return ""
}

func normalizeBandwidth(v string, preferMbps bool) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if _, err := strconv.ParseFloat(v, 64); err == nil {
		if preferMbps {
			return v + " mbps"
		}
		// Most hy2 links expose plain numbers as Mbps.
		return v + " mbps"
	}
	return v
}

func parseALPNValues(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		for _, part := range strings.Split(raw, ",") {
			v := strings.TrimSpace(part)
			if v == "" {
				continue
			}
			if _, ok := seen[v]; ok {
				continue
			}
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	return out
}

func parseTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
