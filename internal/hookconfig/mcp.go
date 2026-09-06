package hookconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// MergeClaudeMCP adds or updates the acr server in a Claude Code .mcp.json,
// keeping every other server.
func MergeClaudeMCP(existing []byte, command string, args []string) ([]byte, error) {
	root := map[string]any{}
	if len(bytes.TrimSpace(existing)) != 0 {
		decoder := json.NewDecoder(bytes.NewReader(existing))
		decoder.UseNumber()
		if err := decoder.Decode(&root); err != nil {
			return nil, fmt.Errorf("decode existing .mcp.json: %w", err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return nil, errors.New("decode existing .mcp.json: multiple JSON values")
		}
	}
	servers, ok := root["mcpServers"].(map[string]any)
	if !ok {
		if _, exists := root["mcpServers"]; exists {
			return nil, errors.New("existing .mcp.json field mcpServers must be an object")
		}
		servers = map[string]any{}
		root["mcpServers"] = servers
	}
	servers["acr"] = map[string]any{"type": "stdio", "command": command, "args": args}
	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// MergeCodexMCP appends the acr server to a project .codex/config.toml. A
// file that already configures acr is returned unchanged, because ACR does
// not rewrite TOML it did not write; changed reports whether it appended.
func MergeCodexMCP(existing []byte, command string, args []string) ([]byte, bool, error) {
	if strings.Contains(string(existing), "[mcp_servers.acr]") {
		return existing, false, nil
	}
	quote := func(value string) string {
		encoded, _ := json.Marshal(value)
		return string(encoded)
	}
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, quote(arg))
	}
	block := "[mcp_servers.acr]\n" +
		"command = " + quote(command) + "\n" +
		"args = [" + strings.Join(quoted, ", ") + "]\n" +
		"# ACR checks every call itself; its tools only change ACR's memories and rules.\n" +
		"default_tools_approval_mode = \"approve\"\n"
	data := append([]byte{}, existing...)
	if len(bytes.TrimSpace(data)) != 0 {
		if !bytes.HasSuffix(data, []byte("\n")) {
			data = append(data, '\n')
		}
		data = append(data, '\n')
	}
	return append(data, block...), true, nil
}
