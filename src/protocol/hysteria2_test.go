package protocol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestParseHysteria2(t *testing.T) {
	link := "hy2://mypassword@example.com:443?sni=cdn.example.com&insecure=1&alpn=h3,h2&upmbps=20&downmbps=80&congestion=bbr&obfs=salamander&obfs-password=secret"

	n, err := parseHysteria2(link)
	if err != nil {
		t.Fatalf("parseHysteria2 failed: %v", err)
	}

	if n.Protocol != "hysteria2" {
		t.Fatalf("unexpected protocol: %s", n.Protocol)
	}
	if n.Address != "example.com" || n.Port != 443 {
		t.Fatalf("unexpected address/port: %s:%d", n.Address, n.Port)
	}
	if n.Password != "mypassword" {
		t.Fatalf("unexpected auth: %q", n.Password)
	}
	if n.SNI != "cdn.example.com" {
		t.Fatalf("unexpected sni: %q", n.SNI)
	}
	if !n.AllowInsecure {
		t.Fatalf("expected allow insecure true")
	}
	if n.Up != "20 mbps" || n.Down != "80 mbps" {
		t.Fatalf("unexpected bandwidth up=%q down=%q", n.Up, n.Down)
	}
	if n.Obfs != "salamander" || n.ObfsPassword != "secret" {
		t.Fatalf("unexpected obfs config: obfs=%q pwd=%q", n.Obfs, n.ObfsPassword)
	}
	if len(n.ALPN) != 2 || n.ALPN[0] != "h3" || n.ALPN[1] != "h2" {
		t.Fatalf("unexpected alpn: %#v", n.ALPN)
	}
}

func TestGenerateXrayConfigHy2(t *testing.T) {
	link := "hy2://mypassword@example.com:443?sni=cdn.example.com&insecure=1&alpn=h3,h2&upmbps=20&downmbps=80&obfs=salamander&obfs-password=secret"
	cfgPath := filepath.Join(t.TempDir(), "hy2.json")

	if err := GenerateXrayConfig(link, 10801, cfgPath); err != nil {
		t.Fatalf("GenerateXrayConfig failed: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config failed: %v", err)
	}

	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal config failed: %v", err)
	}

	outbounds, ok := cfg["outbounds"].([]any)
	if !ok || len(outbounds) != 1 {
		t.Fatalf("unexpected outbounds: %#v", cfg["outbounds"])
	}
	outbound, ok := outbounds[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected outbound object: %#v", outbounds[0])
	}
	if outbound["protocol"] != "hysteria" {
		t.Fatalf("unexpected outbound protocol: %#v", outbound["protocol"])
	}

	settings, ok := outbound["settings"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected settings: %#v", outbound["settings"])
	}
	if settings["address"] != "example.com" {
		t.Fatalf("unexpected settings.address: %#v", settings["address"])
	}
	if int(settings["port"].(float64)) != 443 {
		t.Fatalf("unexpected settings.port: %#v", settings["port"])
	}

	stream, ok := outbound["streamSettings"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected streamSettings: %#v", outbound["streamSettings"])
	}
	if stream["network"] != "hysteria" {
		t.Fatalf("unexpected streamSettings.network: %#v", stream["network"])
	}
	if stream["security"] != "tls" {
		t.Fatalf("unexpected streamSettings.security: %#v", stream["security"])
	}

	hysteriaSettings, ok := stream["hysteriaSettings"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected hysteriaSettings: %#v", stream["hysteriaSettings"])
	}
	if int(hysteriaSettings["version"].(float64)) != 2 {
		t.Fatalf("unexpected hysteriaSettings.version: %#v", hysteriaSettings["version"])
	}
	if hysteriaSettings["auth"] != "mypassword" {
		t.Fatalf("unexpected hysteriaSettings.auth: %#v", hysteriaSettings["auth"])
	}
	if hysteriaSettings["up"] != "20 mbps" || hysteriaSettings["down"] != "80 mbps" {
		t.Fatalf("unexpected hysteriasettings up/down: up=%#v down=%#v", hysteriaSettings["up"], hysteriaSettings["down"])
	}

	tlsSettings, ok := stream["tlsSettings"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected tlsSettings: %#v", stream["tlsSettings"])
	}
	if tlsSettings["serverName"] != "cdn.example.com" {
		t.Fatalf("unexpected tlsSettings.serverName: %#v", tlsSettings["serverName"])
	}
	if tlsSettings["allowInsecure"] != true {
		t.Fatalf("unexpected tlsSettings.allowInsecure: %#v", tlsSettings["allowInsecure"])
	}

	finalmask, ok := stream["finalmask"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected finalmask: %#v", stream["finalmask"])
	}
	udp, ok := finalmask["udp"].([]any)
	if !ok || len(udp) != 1 {
		t.Fatalf("unexpected finalmask.udp: %#v", finalmask["udp"])
	}
	mask, ok := udp[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected finalmask.udp[0]: %#v", udp[0])
	}
	if mask["type"] != "salamander" {
		t.Fatalf("unexpected finalmask type: %#v", mask["type"])
	}
}
