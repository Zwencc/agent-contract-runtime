package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-contract-runtime/acr/internal/project"
)

// A person changes the enforced contracts with acr contract activate and
// retire; afterwards a file changed in the contract directory by other means
// changes nothing until someone activates it.
func TestContractRegistryCommands(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	config, paths, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile("../../examples/contracts/deny-force-push.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.Contracts, "deny-force-push.json"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	contractCommand := func(args ...string) (int, string) {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		code := run(append([]string{"contract"}, append(args, "--root", root)...), strings.NewReader(""), &stdout, &stderr)
		return code, stdout.String() + stderr.String()
	}
	forcePush := func() string {
		t.Helper()
		decision, _, _ := hookDecision(t, "claude", root, config, paths, "Bash", map[string]string{"command": "git push --force origin main"}, "default")
		return decision
	}

	if code, out := contractCommand("list"); code != 0 || !strings.Contains(out, "none was activated through ACR yet") {
		t.Fatalf("unmanaged list = %d %s", code, out)
	}
	if code, out := contractCommand("activate", "../../examples/contracts/shared-generated-to-src.json"); code != 0 || !strings.Contains(out, "preview only") {
		t.Fatalf("activate preview = %d %s", code, out)
	}
	if code, out := contractCommand("activate", "../../examples/contracts/shared-generated-to-src.json", "--apply"); code != 0 || !strings.Contains(out, "activated shared-generated-to-src version 1") {
		t.Fatalf("activate = %d %s", code, out)
	}
	code, out := contractCommand("list")
	if code != 0 || !strings.Contains(out, "deny-force-push v1") || !strings.Contains(out, "[legacy_import]") || !strings.Contains(out, "shared-generated-to-src v1") || !strings.Contains(out, "[cli]") || strings.Contains(out, "not enforced") {
		t.Fatalf("managed list = %d %s", code, out)
	}

	weakened := strings.Replace(string(original), `"mode": "deny"`, `"mode": "none"`, 1)
	weakened = strings.Replace(weakened, `"enforcement": "hard"`, `"enforcement": "advisory"`, 1)
	if err := os.WriteFile(filepath.Join(paths.Contracts, "deny-force-push.json"), []byte(weakened), 0o600); err != nil {
		t.Fatal(err)
	}
	if decision := forcePush(); decision != "deny" {
		t.Fatalf("a contract file edited outside ACR weakened enforcement: %s", decision)
	}
	if _, out := contractCommand("list"); !strings.Contains(out, "not enforced: deny-force-push.json") {
		t.Fatalf("edited file not reported: %s", out)
	}

	if code, out := contractCommand("retire", "deny-force-push"); code != 2 {
		t.Fatalf("retire without reason = %d %s", code, out)
	}
	if code, out := contractCommand("retire", "deny-force-push", "--reason", "team allows it", "--apply"); code != 0 || !strings.Contains(out, "retired deny-force-push") {
		t.Fatalf("retire = %d %s", code, out)
	}
	if decision := forcePush(); decision != "" {
		t.Fatalf("retired contract still enforced: %s", decision)
	}
}

// mcpCall sends one tools/call to acr mcp serve and returns the result text
// and whether it is an error.
func mcpCall(t *testing.T, root, toolset, name string, arguments any) (string, bool) {
	t.Helper()
	call, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": arguments}})
	var stdout, stderr bytes.Buffer
	if code := run([]string{"mcp", "serve", "--root", root, "--agent", "claude", "--toolset", toolset}, bytes.NewReader(append(call, '\n')), &stdout, &stderr); code != 0 {
		t.Fatalf("mcp serve code = %d, stderr = %s", code, stderr.String())
	}
	var response struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil || len(response.Result.Content) != 1 {
		t.Fatalf("mcp response %q: %v", stdout.String(), err)
	}
	return response.Result.Content[0].Text, response.Result.IsError
}

// The user tells Claude Code "remember: nobody changes the orders database
// unless I say so". The rule then holds for every agent, and lifts for one
// turn only when the user says so in that turn.
func TestUserRuleWithTurnPermission(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	config, paths, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	say := func(text string) {
		t.Helper()
		promptInSession(t, "claude", "claude-session", root, text, config, paths)
	}
	deleteOrders := func(agent string) (string, string) {
		t.Helper()
		decision, reason, _ := hookDecision(t, agent, root, config, paths, "Bash", map[string]string{"command": "psql orders -c 'delete from test_orders'"}, "default")
		return decision, reason
	}
	rule := map[string]any{
		"apiVersion": "acr.dev/v1alpha1", "kind": "Contract",
		"metadata":      map[string]any{"id": "protect-orders-db", "version": 1, "title": "orders 库不能自己改", "status": "active", "severity": "critical", "priority": 950},
		"scope":         map[string]any{"operation_kinds": []string{"process.exec"}},
		"applicability": map[string]any{"all": []map[string]any{{"field": "input.command", "operator": "contains", "value": "orders"}}},
		"behavior":      map[string]any{"instruction": "orders 库不能自己改，除非用户明确让你改", "enforcement": "hard"},
		"correction":    map[string]any{"mode": "deny"},
		"authorization": map[string]any{"user_in_turn": true, "mentions": []string{"orders"}},
		"failure_mode":  "closed",
	}

	say("你记住，orders 库不能自己改，除非我主动让你改")
	if text, failed := mcpCall(t, root, "directives", "add_rule", map[string]any{"contract": rule, "quote": "orders 库不能自己改，除非我主动让你改"}); failed {
		t.Fatalf("add_rule = %s", text)
	}
	decision, reason := deleteOrders("claude")
	if decision != "deny" || !strings.Contains(reason, "mcp__acr__authorize") {
		t.Fatalf("before permission = %s %q", decision, reason)
	}

	say("这次可以改 orders 库，把测试订单删掉")
	if text, failed := mcpCall(t, root, "directives", "authorize", map[string]any{"contract_id": "protect-orders-db", "quote": "这次可以改 orders 库"}); failed {
		t.Fatalf("authorize = %s", text)
	}
	if decision, reason := deleteOrders("claude"); decision != "" {
		t.Fatalf("with permission = %s %q", decision, reason)
	}
	if decision, _ := deleteOrders("codex"); decision != "deny" {
		t.Fatalf("permission leaked to another agent's session: %s", decision)
	}

	say("好的，继续别的事")
	if decision, _ := deleteOrders("claude"); decision != "deny" {
		t.Fatalf("permission outlived the turn: %s", decision)
	}
}

// What the user asks one agent to remember or enforce reaches the other
// running sessions on their next prompt, once; the session that made the
// change is not told again.
func TestOtherSessionsLearnNewMemoriesAndRules(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	config, paths, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	promptInSession(t, "codex", "codex-1", root, "看一下登录模块", config, paths)
	promptInSession(t, "claude", "claude-1", root, "记住：以后发布都用 ./scripts/release.sh，并且禁止强制推送", config, paths)
	if text, failed := mcpCall(t, root, "directives", "remember", map[string]any{"claim": "Release with ./scripts/release.sh", "quote": "以后发布都用 ./scripts/release.sh"}); failed {
		t.Fatalf("remember = %s", text)
	}
	rule, _ := os.ReadFile("../../examples/contracts/deny-force-push.json")
	var contractValue map[string]any
	json.Unmarshal(rule, &contractValue)
	if text, failed := mcpCall(t, root, "directives", "add_rule", map[string]any{"contract": contractValue, "quote": "禁止强制推送"}); failed {
		t.Fatalf("add_rule = %s", text)
	}

	notice := hookContext(t, promptInSession(t, "codex", "codex-1", root, "继续", config, paths))
	if !strings.Contains(notice, `"Release with ./scripts/release.sh" now applies`) || !strings.Contains(notice, "[new rule] deny-force-push") {
		t.Fatalf("other session notice:\n%s", notice)
	}
	if again := promptInSession(t, "codex", "codex-1", root, "下一步", config, paths); strings.TrimSpace(again) != "{}" {
		t.Fatalf("notice repeated: %s", again)
	}
	if own := promptInSession(t, "claude", "claude-1", root, "好了", config, paths); strings.TrimSpace(own) != "{}" {
		t.Fatalf("the session that made the change was told again: %s", own)
	}

	// The person reviews what the agents recorded, with the words behind it.
	if text, failed := mcpCall(t, root, "directives", "remember", map[string]any{"claim": "Login tests are slow"}); failed {
		t.Fatalf("candidate = %s", text)
	}
	stdout.Reset()
	if code := run([]string{"memory", "review", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("review code = %d, stderr = %s", code, stderr.String())
	}
	review := stdout.String()
	for _, want := range []string{
		`[in effect]`, `"Release with ./scripts/release.sh" (source: user) — user asked: "以后发布都用 ./scripts/release.sh"`,
		`[rule added] deny-force-push v1`, `user said: "禁止强制推送"`,
		`[candidate]`, `"Login tests are slow" (source: agent) — the agent proposed this`,
	} {
		if !strings.Contains(review, want) {
			t.Fatalf("review lacks %q:\n%s", want, review)
		}
	}
}
