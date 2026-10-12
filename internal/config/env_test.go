package config

import "testing"

func TestIsLoopbackAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:46750": true,
		"127.0.0.2:1":     true,
		"[::1]:9":         true,
		"localhost:0":     true,
		"LOCALHOST:80":    true,
		"0.0.0.0:46750":   false,
		":46750":          false,
		"192.168.1.10:1":  false,
		"example.com:80":  false,
		"127.0.0.1":       false,
		"":                false,
	} {
		if got := IsLoopbackAddr(addr); got != want {
			t.Errorf("IsLoopbackAddr(%q) = %v，期望 %v", addr, got, want)
		}
	}
}

func TestChildEnvAllowlistIsCopy(t *testing.T) {
	a := ChildEnvAllowlist()
	a[0] = "changed"
	if ChildEnvAllowlist()[0] == "changed" {
		t.Fatal("ChildEnvAllowlist 应返回副本")
	}
	for _, k := range []string{"PATH", "HTTPS_PROXY", "no_proxy", "SSL_CERT_FILE"} {
		found := false
		for _, v := range ChildEnvAllowlist() {
			if v == k {
				found = true
			}
		}
		if !found {
			t.Errorf("白名单缺少 %s", k)
		}
	}
}
