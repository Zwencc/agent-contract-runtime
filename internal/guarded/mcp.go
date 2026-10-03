package guarded

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
)

// supportedProtocolVersions are the MCP revisions this server speaks; it
// answers with the client's version when supported and its newest otherwise.
var supportedProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

const serverInstructions = "These tools replace the native shell and file-editing tools. " +
	"ACR checks every call against the project's contracts before it runs; a refused call did not run, and its message says why and what to do."

type rpcRequest struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Tools describes the guarded tools in MCP tools/list form.
func Tools() []map[string]any {
	object := func(required []string, properties map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	text := func(description string) map[string]any { return map[string]any{"type": "string", "description": description} }
	return []map[string]any{
		{
			"name":        ToolExec,
			"description": "Run a shell command in the project after ACR checks it against the project contracts. Returns exit code, stdout and stderr.",
			"inputSchema": object([]string{"command"}, map[string]any{
				"command":         text("Shell command, run with /bin/sh -c"),
				"cwd":             text("Working directory relative to the project root"),
				"timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 600},
			}),
		},
		{
			"name":        ToolWriteFile,
			"description": "Create or replace a file inside the project after ACR checks it against the project contracts.",
			"inputSchema": object([]string{"file_path", "content"}, map[string]any{
				"file_path": text("Absolute path, or a path relative to the project root"),
				"content":   text("Complete new file content"),
			}),
		},
		{
			"name":        ToolEditFile,
			"description": "Replace text in a project file after ACR checks it against the project contracts. old_string must occur exactly once unless replace_all is set.",
			"inputSchema": object([]string{"file_path", "old_string", "new_string"}, map[string]any{
				"file_path":   text("Absolute path, or a path relative to the project root"),
				"old_string":  text("Exact text to replace"),
				"new_string":  text("Replacement text"),
				"replace_all": map[string]any{"type": "boolean"},
			}),
		},
	}
}

// Toolset is a group of tools the MCP server offers.
type Toolset interface {
	Tools() []map[string]any
	Call(ctx context.Context, name string, arguments json.RawMessage) Result
	Instructions() string
}

// Tools lists the guarded execution tools.
func (executor Executor) Tools() []map[string]any { return Tools() }

// Instructions tells the agent what the guarded tools are for.
func (executor Executor) Instructions() string { return serverInstructions }

// Toolsets serves several toolsets as one.
type Toolsets []Toolset

func (sets Toolsets) Tools() []map[string]any {
	tools := []map[string]any{}
	for _, set := range sets {
		tools = append(tools, set.Tools()...)
	}
	return tools
}

func (sets Toolsets) Call(ctx context.Context, name string, arguments json.RawMessage) Result {
	for _, set := range sets {
		for _, tool := range set.Tools() {
			if tool["name"] == name {
				return set.Call(ctx, name, arguments)
			}
		}
	}
	return failure("unknown tool %q", name)
}

func (sets Toolsets) Instructions() string {
	parts := []string{}
	for _, set := range sets {
		parts = append(parts, set.Instructions())
	}
	return strings.Join(parts, "\n\n")
}

// Serve runs the MCP stdio transport: one JSON-RPC message per line on in,
// responses on out. Notifications get no response.
func Serve(ctx context.Context, in io.Reader, out io.Writer, tools Toolset, version string) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64<<10), 16<<20)
	encoder := json.NewEncoder(out)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var request rpcRequest
		if err := json.Unmarshal(line, &request); err != nil {
			if err := encoder.Encode(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}}); err != nil {
				return err
			}
			continue
		}
		if len(request.ID) == 0 {
			continue
		}
		response := rpcResponse{JSONRPC: "2.0", ID: request.ID}
		switch request.Method {
		case "initialize":
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			json.Unmarshal(request.Params, &params)
			negotiated := supportedProtocolVersions[0]
			if slices.Contains(supportedProtocolVersions, params.ProtocolVersion) {
				negotiated = params.ProtocolVersion
			}
			response.Result = map[string]any{
				"protocolVersion": negotiated,
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
				"serverInfo":      map[string]string{"name": "acr", "version": version},
				"instructions":    tools.Instructions(),
			}
		case "ping":
			response.Result = map[string]any{}
		case "tools/list":
			response.Result = map[string]any{"tools": tools.Tools()}
		case "tools/call":
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(request.Params, &params); err != nil {
				response.Error = &rpcError{Code: -32602, Message: "invalid tools/call params"}
				break
			}
			if len(params.Arguments) == 0 {
				params.Arguments = json.RawMessage(`{}`)
			}
			result := tools.Call(ctx, params.Name, params.Arguments)
			response.Result = map[string]any{
				"content": []map[string]string{{"type": "text", "text": result.Text}},
				"isError": result.IsError,
			}
		default:
			response.Error = &rpcError{Code: -32601, Message: "method not found: " + request.Method}
		}
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
	return scanner.Err()
}
