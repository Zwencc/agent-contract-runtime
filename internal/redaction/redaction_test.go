package redaction

import (
	"strings"
	"testing"
)

func TestJSONRedactsSensitiveKeysAndKnownCredentialValues(t *testing.T) {
	input := []byte(`{
  "password":"correct horse battery staple",
  "nested":{"api_key":"key-value","safe":"Bearer abcdefghijklmnop"},
  "command":"curl -H 'Authorization: Bearer abcdefghijklmnop' https://example.test",
  "identifier":9007199254740993
}`)
	output, err := JSON(input)
	if err != nil {
		t.Fatalf("JSON() error = %v", err)
	}
	text := string(output)
	for _, secret := range []string{"correct horse", "key-value", "abcdefghijklmnop"} {
		if strings.Contains(text, secret) {
			t.Fatalf("redacted output contains %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, `9007199254740993`) {
		t.Fatalf("redaction lost number precision: %s", text)
	}
}

func TestJSONRedactsPrivateKeyAsWholeValue(t *testing.T) {
	output, err := JSON([]byte(`{"value":"-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----"}`))
	if err != nil {
		t.Fatalf("JSON() error = %v", err)
	}
	if strings.Contains(string(output), "PRIVATE KEY") || strings.Contains(string(output), "secret") {
		t.Fatalf("private key was not redacted: %s", output)
	}
}
