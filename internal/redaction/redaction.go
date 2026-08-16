// Package redaction removes common credentials before data enters durable storage.
package redaction

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"
)

const Replacement = "[REDACTED]"

var valuePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]{12,}`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{12,}\b`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`),
}

func JSON(data []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode JSON for redaction: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode JSON for redaction: multiple values")
	}
	redacted := redactValue(value)
	encoded, err := json.Marshal(redacted)
	if err != nil {
		return nil, fmt.Errorf("encode redacted JSON: %w", err)
	}
	return encoded, nil
}

func redactValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		output := make(map[string]any, len(typed))
		for key, child := range typed {
			if sensitiveKey(key) {
				output[key] = Replacement
			} else {
				output[key] = redactValue(child)
			}
		}
		return output
	case []any:
		output := make([]any, len(typed))
		for index, child := range typed {
			output[index] = redactValue(child)
		}
		return output
	case string:
		return redactString(typed)
	default:
		return value
	}
}

func sensitiveKey(key string) bool {
	normalized := strings.Map(func(value rune) rune {
		if unicode.IsLetter(value) || unicode.IsDigit(value) {
			return unicode.ToLower(value)
		}
		return -1
	}, key)
	for _, marker := range []string{"password", "passwd", "secret", "token", "apikey", "authorization", "credential", "privatekey"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func redactString(value string) string {
	upper := strings.ToUpper(value)
	if strings.Contains(upper, "-----BEGIN") && strings.Contains(upper, "PRIVATE KEY-----") {
		return Replacement
	}
	for _, pattern := range valuePatterns {
		if strings.HasPrefix(strings.ToLower(pattern.String()), "(?i)(bearer") {
			value = pattern.ReplaceAllString(value, `${1}`+Replacement)
		} else {
			value = pattern.ReplaceAllString(value, Replacement)
		}
	}
	return value
}
