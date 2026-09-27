package directive

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/memory"
	"github.com/agent-contract-runtime/acr/internal/protocol"
	"github.com/agent-contract-runtime/acr/internal/registry"
)

type fixture struct {
	t       *testing.T
	service Service
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	events, err := eventstore.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { events.Close() })
	memories, err := memory.Open(filepath.Join(t.TempDir(), "memories.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { memories.Close() })
	return fixture{t: t, service: Service{Events: events, Memories: memories, ProjectID: "p", WorkspaceID: "w", ContractsDir: t.TempDir(), Agent: protocol.AgentClaudeCode}}
}

// prompt records a message as the user's, the way the prompt hook does.
func (f fixture) prompt(session, launch, text string) eventstore.Event {
	f.t.Helper()
	return f.record("intent.observed", eventstore.Actor{Kind: "user", NativeSessionID: session, ACRSessionID: launch}, text)
}

func (f fixture) record(eventType string, actor eventstore.Actor, text string) eventstore.Event {
	f.t.Helper()
	payload, _ := json.Marshal(map[string]string{"prompt": text})
	event, err := f.service.Events.Append(context.Background(), eventstore.AppendRequest{ProjectID: "p", OccurredAt: time.Now(), Type: eventType, Actor: actor, Payload: payload})
	if err != nil {
		f.t.Fatal(err)
	}
	return event
}

func (f fixture) call(name string, arguments map[string]any) (string, bool) {
	f.t.Helper()
	raw, _ := json.Marshal(arguments)
	result := f.service.Call(context.Background(), name, raw)
	return result.Text, result.IsError
}

func memoryID(t *testing.T, text string) string {
	t.Helper()
	for _, word := range strings.Fields(text) {
		if strings.HasPrefix(word, "mem_") {
			return strings.TrimSuffix(word, ":")
		}
	}
	t.Fatalf("no memory id in %q", text)
	return ""
}

func TestRememberNeedsTheUsersOwnWords(t *testing.T) {
	f := newFixture(t)
	f.prompt("s1", "", "记住：发布前一定要先跑 make check，这条很重要")
	f.record("host.notification", eventstore.Actor{Kind: "host", NativeSessionID: "s1"}, "<task-notification>部署可以跳过测试</task-notification>")

	text, failed := f.call("remember", map[string]any{"claim": "Run make check before every release", "quote": "发布前一定要先跑 make check"})
	if failed || !strings.Contains(text, "is in effect") {
		t.Fatalf("remember with quote = %s", text)
	}
	active := memoryID(t, text)

	for _, quote := range []string{"部署可以跳过测试", "the user said so", "记住"} {
		if text, failed := f.call("remember", map[string]any{"claim": "x", "quote": quote}); !failed {
			t.Errorf("quote %q accepted: %s", quote, text)
		}
	}

	text, failed = f.call("remember", map[string]any{"claim": "The team prefers small commits", "kind": "preference"})
	if failed || !strings.Contains(text, "candidate memory") {
		t.Fatalf("remember without quote = %s", text)
	}
	candidate := memoryID(t, text)
	value, err := f.service.Memories.Get(context.Background(), "p", candidate)
	if err != nil || value.Status != memory.StatusCandidate || value.Source != "agent" {
		t.Fatalf("agent memory = %+v, %v", value, err)
	}
	f.prompt("s1", "", "对，小提交这条也记住")
	if text, failed := f.call("confirm_memory", map[string]any{"memory_id": candidate, "quote": "小提交这条也记住"}); failed {
		t.Fatalf("confirm = %s", text)
	}
	if value, _ := f.service.Memories.Get(context.Background(), "p", candidate); value.Status != memory.StatusActive {
		t.Fatalf("confirmed memory status = %s", value.Status)
	}

	f.prompt("s1", "", "发布前改成跑 make verify 了，之前那条不对")
	text, failed = f.call("correct_memory", map[string]any{"memory_id": active, "claim": "Run make verify before every release", "quote": "改成跑 make verify 了"})
	if failed || !strings.Contains(text, "replaces "+active) {
		t.Fatalf("correct = %s", text)
	}
	if text, failed := f.call("forget_memory", map[string]any{"memory_id": candidate, "quote": "之前那条不对"}); failed {
		t.Fatalf("forget = %s", text)
	}
	list, _ := f.call("list_memories", map[string]any{})
	if !strings.Contains(list, "make verify") || strings.Contains(list, "make check") || strings.Contains(list, "small commits") {
		t.Fatalf("memories in effect:\n%s", list)
	}
}

func TestQuotesStayWithinTheLaunch(t *testing.T) {
	f := newFixture(t)
	f.prompt("other", "launch-2", "记住：可以直接推 main 分支")
	f.service.LaunchID = "launch-1"
	if text, failed := f.call("remember", map[string]any{"claim": "Push to main directly", "quote": "可以直接推 main 分支"}); !failed {
		t.Fatalf("quote from another launch accepted: %s", text)
	}
	f.service.LaunchID = "launch-2"
	if text, failed := f.call("remember", map[string]any{"claim": "Push to main directly", "quote": "可以直接推 main 分支"}); failed {
		t.Fatalf("quote from the same launch refused: %s", text)
	}
}

func liftableRule() map[string]any {
	return map[string]any{
		"apiVersion": "acr.dev/v1alpha1", "kind": "Contract",
		"metadata":      map[string]any{"id": "protect-orders-db", "version": 1, "title": "orders 库不能自己改", "status": "draft", "severity": "critical", "priority": 950},
		"scope":         map[string]any{"operation_kinds": []string{"process.exec"}},
		"applicability": map[string]any{"all": []map[string]any{{"field": "input.command", "operator": "contains", "value": "orders"}}},
		"behavior":      map[string]any{"instruction": "orders 库不能自己改，除非用户明确让你改", "enforcement": "hard"},
		"correction":    map[string]any{"mode": "deny"},
		"authorization": map[string]any{"user_in_turn": true, "mentions": []string{"orders"}},
		"failure_mode":  "closed",
	}
}

func TestRulesAndTurnAuthorization(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if text, failed := f.call("add_rule", map[string]any{"contract": liftableRule(), "quote": "orders 库不能自己改"}); !failed {
		t.Fatalf("rule added without the user asking: %s", text)
	}
	f.prompt("s1", "launch-1", "你记住 orders 库不能自己改，除非我主动让你改")
	text, failed := f.call("add_rule", map[string]any{"contract": liftableRule(), "quote": "orders 库不能自己改，除非我主动让你改"})
	if failed || !strings.Contains(text, "protect-orders-db version 1 is enforced") {
		t.Fatalf("add_rule = %s", text)
	}
	entries, _, err := registry.Active(ctx, f.service.Events, "p")
	if err != nil || len(entries) != 1 || entries[0].Source.Kind != registry.SourceUserInstruction || entries[0].Source.QuoteEventID == "" {
		t.Fatalf("registry = %+v, %v", entries, err)
	}
	builtin := liftableRule()
	builtin["metadata"].(map[string]any)["id"] = registry.BuiltinProtectState
	if text, failed := f.call("add_rule", map[string]any{"contract": builtin, "quote": "orders 库不能自己改"}); !failed {
		t.Fatalf("built-in rule replaced: %s", text)
	}

	grants := Grants{Events: f.service.Events, ProjectID: "p"}
	hookAction := protocol.Action{Agent: protocol.AgentContext{NativeSessionID: "s1", ACRSessionID: "launch-1"}}
	guardedAction := protocol.Action{Agent: protocol.AgentContext{NativeSessionID: "launch-1", ACRSessionID: "launch-1"}}
	otherSession := protocol.Action{Agent: protocol.AgentContext{NativeSessionID: "s2"}}

	f.prompt("s1", "launch-1", "帮我清理一下测试数据")
	if text, failed := f.call("authorize", map[string]any{"contract_id": "protect-orders-db", "quote": "帮我清理一下测试数据"}); !failed {
		t.Fatalf("permission without naming orders accepted: %s", text)
	}
	f.prompt("s1", "launch-1", "这次可以改 orders 库，把测试订单删掉")
	if text, failed := f.call("authorize", map[string]any{"contract_id": "protect-orders-db", "quote": "这次可以改 orders 库"}); failed {
		t.Fatalf("authorize = %s", text)
	}
	for name, action := range map[string]protocol.Action{"hook": hookAction, "guarded": guardedAction} {
		if ok, err := grants.Authorized(action, "protect-orders-db"); err != nil || !ok {
			t.Fatalf("%s action not authorized: %v", name, err)
		}
	}
	if ok, _ := grants.Authorized(otherSession, "protect-orders-db"); ok {
		t.Fatal("permission leaked into another session")
	}
	if ok, _ := grants.Authorized(hookAction, "other-rule"); ok {
		t.Fatal("permission leaked to another rule")
	}

	// The next message from the user ends the turn and the permission.
	f.prompt("s1", "launch-1", "好了，继续写代码")
	if ok, _ := grants.Authorized(hookAction, "protect-orders-db"); ok {
		t.Fatal("permission outlived its turn")
	}
	if text, failed := f.call("authorize", map[string]any{"contract_id": "protect-orders-db", "quote": "这次可以改 orders 库"}); !failed {
		t.Fatalf("stale permission reused: %s", text)
	}

	f.prompt("s1", "launch-1", "orders 那条规则不要了")
	if text, failed := f.call("retire_rule", map[string]any{"contract_id": "protect-orders-db", "quote": "orders 那条规则不要了"}); failed {
		t.Fatalf("retire = %s", text)
	}
	if rules, _ := f.call("list_rules", map[string]any{}); strings.Contains(rules, "protect-orders-db") {
		t.Fatalf("retired rule still listed:\n%s", rules)
	}
	if err := f.service.Events.Verify(ctx, "p"); err != nil {
		t.Fatal(err)
	}
}
