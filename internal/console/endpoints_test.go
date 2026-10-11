package console

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/memory"
	"github.com/agent-contract-runtime/acr/internal/protocol"
)

type fixture struct {
	t      *testing.T
	server *Server
	cookie *http.Cookie
}

func newFixture(t *testing.T) fixture {
	server, _ := testServer(t)
	cookie := request(t, server, http.MethodGet, "/", nil).Result().Cookies()[0]
	return fixture{t: t, server: server, cookie: cookie}
}

func (f fixture) add(requests ...eventstore.AppendRequest) []eventstore.Event {
	f.t.Helper()
	for index := range requests {
		requests[index].ProjectID = f.server.config.ProjectID
		if requests[index].OccurredAt.IsZero() {
			requests[index].OccurredAt = time.Now()
		}
		if requests[index].Payload == nil {
			requests[index].Payload = json.RawMessage(`{}`)
		}
	}
	events, err := f.server.events.AppendBatch(context.Background(), requests)
	if err != nil {
		f.t.Fatal(err)
	}
	return events
}

func (f fixture) get(path string, target any) *http.Response {
	f.t.Helper()
	response := request(f.t, f.server, http.MethodGet, path, nil)
	if target != nil {
		if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
			f.t.Fatalf("GET %s: %d %s (%v)", path, response.Code, response.Body.String(), err)
		}
	}
	return response.Result()
}

func (f fixture) send(method, path string, body any, target any) int {
	f.t.Helper()
	encoded, _ := json.Marshal(body)
	response := request(f.t, f.server, method, path, bytes.NewReader(encoded), f.cookie)
	if target != nil && response.Code < 300 {
		if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
			f.t.Fatalf("%s %s: %s (%v)", method, path, response.Body.String(), err)
		}
	}
	return response.Code
}

func actionPayload(t *testing.T, agent protocol.AgentKind, session, tool, operation string, input map[string]string, resources ...string) json.RawMessage {
	t.Helper()
	raw, _ := json.Marshal(input)
	action := protocol.NewAction(protocol.AgentContext{Kind: agent, NativeSessionID: session}, protocol.WorkspaceContext{CWD: "/w", Root: "/w"}, protocol.ToolCall{Name: tool, Input: raw})
	action.OperationKind, action.Resources = operation, resources
	encoded, _ := json.Marshal(action)
	return encoded
}

func TestEventDetailCursorSessionAndReplay(t *testing.T) {
	f := newFixture(t)
	events := f.add(
		eventstore.AppendRequest{EventID: "launch-1", Type: "runtime.launch_started", Actor: eventstore.Actor{Kind: "runtime", AgentKind: "claude-code"}, Payload: json.RawMessage(`{"mode":"native"}`)},
		eventstore.AppendRequest{Type: "intent.observed", Actor: eventstore.Actor{Kind: "user", AgentKind: "claude-code", NativeSessionID: "claude-1", ACRSessionID: "launch-1"}, Payload: json.RawMessage(`{"prompt":"改分页"}`)},
		eventstore.AppendRequest{Type: "action.completed", Actor: eventstore.Actor{Kind: "agent", AgentKind: "claude-code", NativeSessionID: "launch-1", ACRSessionID: "launch-1"}},
		eventstore.AppendRequest{Type: "action.completed", Actor: eventstore.Actor{Kind: "agent", AgentKind: "codex", NativeSessionID: "codex-9"}},
	)
	var detail eventDetailView
	f.get("/api/v1/events/"+f.server.config.ProjectID+"/"+itoa(events[1].Sequence), &detail)
	if detail.ID != events[1].EventID || detail.Actor.NativeSessionID != "claude-1" || !strings.Contains(string(detail.Payload), "改分页") {
		t.Fatalf("event detail = %+v", detail)
	}
	if code := request(t, f.server, http.MethodGet, "/api/v1/events/other-project/1", nil).Code; code != http.StatusNotFound {
		t.Fatalf("other project = %d", code)
	}

	var page []eventView
	response := f.get("/api/v1/events?after=0&limit=2", &page)
	if len(page) != 2 || page[0].Sequence != 1 || response.Header.Get("X-Next-After") != "2" {
		t.Fatalf("first page = %+v, next %q", page, response.Header.Get("X-Next-After"))
	}
	response = f.get("/api/v1/events?after=2&limit=2&type=action.completed", &page)
	if len(page) != 2 || page[0].Type != "action.completed" || response.Header.Get("X-Next-After") != "" {
		t.Fatalf("typed page = %+v, next %q", page, response.Header.Get("X-Next-After"))
	}

	var session struct {
		Session sessionView `json:"session"`
		Events  []eventView `json:"events"`
	}
	f.get("/api/v1/sessions/claude-1", &session)
	if session.Session.ID != "launch-1" || len(session.Events) != 2 {
		t.Fatalf("session detail = %+v", session)
	}
	var replay struct {
		Events []eventDetailView `json:"events"`
	}
	f.get("/api/v1/replay/claude-1", &replay)
	if len(replay.Events) != 3 || replay.Events[0].Type != "runtime.launch_started" || replay.Events[2].Actor.NativeSessionID != "launch-1" {
		t.Fatalf("replay = %+v", replay.Events)
	}
}

func itoa(value uint64) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func TestContractDraftSimulateActivateDeactivate(t *testing.T) {
	f := newFixture(t)
	f.add(eventstore.AppendRequest{Type: "action.proposed", Actor: eventstore.Actor{Kind: "agent", AgentKind: "codex", NativeSessionID: "s"},
		Payload: actionPayload(t, protocol.AgentCodex, "s", "Bash", "process.exec", map[string]string{"command": "git push --force origin main"})})
	draft := map[string]any{
		"apiVersion": "acr.dev/v1alpha1", "kind": "Contract",
		"metadata":      map[string]any{"id": "deny-force-push", "version": 1, "title": "禁止强制推送", "status": "active", "severity": "high", "priority": 900},
		"scope":         map[string]any{"operation_kinds": []string{"process.exec"}},
		"applicability": map[string]any{"all": []map[string]any{{"field": "input.command", "operator": "contains", "value": "git push --force"}}},
		"behavior":      map[string]any{"instruction": "不得强制推送", "enforcement": "hard"},
		"correction":    map[string]any{"mode": "deny"},
		"failure_mode":  "closed",
	}
	var simulated simulationView
	if code := f.send(http.MethodPost, "/api/v1/contracts/deny-force-push/simulate", map[string]any{"contract": draft}, &simulated); code != http.StatusOK || simulated.Evaluated != 1 || simulated.Blocked != 1 {
		t.Fatalf("draft simulation = %d %+v", code, simulated)
	}
	var created struct {
		State string `json:"state"`
	}
	if code := f.send(http.MethodPost, "/api/v1/contracts/drafts", map[string]any{"contract": draft}, &created); code != http.StatusOK || created.State != "draft" {
		t.Fatalf("draft = %d %+v", code, created)
	}
	var activated contractView
	if code := f.send(http.MethodPost, "/api/v1/contracts/deny-force-push/approve", map[string]any{}, &activated); code != http.StatusOK || activated.State != "active" {
		t.Fatalf("approve = %d %+v", code, activated)
	}
	var detail struct {
		Enforced bool        `json:"enforced"`
		History  []eventView `json:"history"`
	}
	f.get("/api/v1/contracts/deny-force-push", &detail)
	if !detail.Enforced || len(detail.History) != 1 {
		t.Fatalf("contract detail = %+v", detail)
	}
	if code := f.send(http.MethodPost, "/api/v1/contracts/deny-force-push/deactivate", map[string]any{}, nil); code != http.StatusBadRequest {
		t.Fatalf("deactivate without reason = %d", code)
	}
	if code := f.send(http.MethodPost, "/api/v1/contracts/deny-force-push/deactivate", map[string]any{"reason": "不再需要"}, nil); code != http.StatusOK {
		t.Fatalf("deactivate = %d", code)
	}
	if code := f.send(http.MethodPost, "/api/v1/contracts/acr-builtin-protect-state/deactivate", map[string]any{"reason": "x"}, nil); code != http.StatusConflict {
		t.Fatalf("deactivating a built-in contract = %d", code)
	}
}

func TestMemoryCorrectionAndCompaction(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	add := func(claim string, evidence string) memory.Memory {
		t.Helper()
		value, err := f.server.memories.CreateCandidate(ctx, memory.CreateRequest{ProjectID: f.server.config.ProjectID, Kind: "constraint", Priority: memory.P1,
			Claim: claim, Scope: json.RawMessage(`{}`), EvidenceRefs: []string{evidence}, Source: "user", Confidence: 1})
		if err != nil {
			t.Fatal(err)
		}
		value, err = f.server.memories.Activate(ctx, f.server.config.ProjectID, value.MemoryID, "test")
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	first := add("Deploy with make release", "event:a")
	second := add("deploy   with make RELEASE", "event:b")
	other := add("Tests need Go 1.27", "event:c")

	var plan compactionView
	if code := f.send(http.MethodPost, "/api/v1/memories/compact/preview", map[string]any{}, &plan); code != http.StatusOK || len(plan.Groups) != 1 || plan.After != plan.Before-1 || plan.Groups[0].Keep != first.MemoryID {
		t.Fatalf("plan = %d %+v", code, plan)
	}
	if code := f.send(http.MethodPost, "/api/v1/memories/compact/apply", map[string]any{}, &plan); code != http.StatusOK {
		t.Fatalf("apply = %d", code)
	}
	kept, _ := f.server.memories.Get(ctx, f.server.config.ProjectID, first.MemoryID)
	merged, _ := f.server.memories.Get(ctx, f.server.config.ProjectID, second.MemoryID)
	if kept.Status != memory.StatusConfirmed || strings.Join(kept.EvidenceRefs, ",") != "event:a,event:b" || merged.Status != memory.StatusArchived {
		t.Fatalf("after compaction kept = %+v, merged = %s", kept, merged.Status)
	}

	var corrected memoryView
	if code := f.send(http.MethodPost, "/api/v1/memories/"+other.MemoryID+"/correct", map[string]any{"claim": "Tests need Go 1.28", "reason": "toolchain upgraded"}, &corrected); code != http.StatusOK || corrected.Claim != "Tests need Go 1.28" {
		t.Fatalf("correct = %d %+v", code, corrected)
	}
	var detail struct {
		Revisions []memory.Revision `json:"revisions"`
	}
	f.get("/api/v1/memories/"+other.MemoryID, &detail)
	if len(detail.Revisions) != 3 || detail.Revisions[2].Action != memory.ActionSupersede {
		t.Fatalf("memory history = %+v", detail.Revisions)
	}
}

func TestAgentInsightsAndReliance(t *testing.T) {
	f := newFixture(t)
	claude := func(eventType, toolUse string, payload json.RawMessage) eventstore.AppendRequest {
		return eventstore.AppendRequest{Type: eventType, Actor: eventstore.Actor{Kind: "agent", AgentKind: "claude-code", NativeSessionID: "c1"},
			Causality: eventstore.Causality{NativeToolUseID: toolUse}, Payload: payload}
	}
	f.add(
		claude("action.proposed", "r1", actionPayload(t, protocol.AgentClaudeCode, "c1", "Read", "filesystem.read", map[string]string{"file_path": "/w/a.go"}, "/w/a.go")),
		claude("action.proposed", "r2", actionPayload(t, protocol.AgentClaudeCode, "c1", "Read", "filesystem.read", map[string]string{"file_path": "/w/a.go"}, "/w/a.go")),
		claude("action.proposed", "w1", actionPayload(t, protocol.AgentClaudeCode, "c1", "Write", "filesystem.write", map[string]string{"file_path": "/w/a.go"}, "/w/a.go")),
		claude("action.rewritten", "w1", nil),
		claude("action.completed", "w1", nil),
		claude("action.proposed", "t1", actionPayload(t, protocol.AgentClaudeCode, "c1", "Bash", "process.exec", map[string]string{"command": "go test ./..."})),
		claude("action.asked", "d1", json.RawMessage(`{"kind":"ask","reason":"生产库写入需要确认","matches":[{"contract_id":"ask-prod"}]}`)),
		claude("action.completed", "d1", nil),
		eventstore.AppendRequest{Type: "verification.passed", Actor: eventstore.Actor{Kind: "runtime"}},
		claude("action.asked", "d2", json.RawMessage(`{"kind":"ask","reason":"还要确认","matches":[{"contract_id":"ask-prod"}]}`)),
	)
	var insights []agentInsightView
	f.get("/api/v1/insights/agents", &insights)
	view := insights[0]
	if view.Agent != "claude-code" || view.Sessions != 1 || view.RepeatedAnalysis != 50 || view.Verification != 100 || view.RepairSuccess != 100 || view.FirstPass != 100 {
		t.Fatalf("claude insight = %+v", view)
	}
	var reliance relianceView
	f.get("/api/v1/insights/reliance", &reliance)
	if reliance.AppropriateAcceptance != 1 || reliance.Known != 1 || reliance.Unknown != 1 || len(reliance.Observations) != 2 {
		t.Fatalf("reliance = %+v", reliance)
	}
}

func TestInteractionStateIsOptInCorrectableAndClearable(t *testing.T) {
	f := newFixture(t)
	f.add(eventstore.AppendRequest{Type: "intent.observed", Actor: eventstore.Actor{Kind: "user", AgentKind: "codex", NativeSessionID: "s"}, Payload: json.RawMessage(`{"prompt":"怎么又失败了，赶紧修好"}`)})
	var signals []interactionSignal
	f.get("/api/v1/insights/interaction-state", &signals)
	if len(signals) != 0 {
		t.Fatalf("signals while disabled = %+v", signals)
	}
	if code := f.send(http.MethodPut, "/api/v1/settings/interaction-state", map[string]bool{"enabled": true}, nil); code != http.StatusOK {
		t.Fatalf("enable = %d", code)
	}
	f.get("/api/v1/insights/interaction-state", &signals)
	kinds := map[string]string{}
	for _, signal := range signals {
		kinds[signal.Kind] = signal.ID
	}
	if kinds["frustration"] == "" || kinds["urgency"] == "" {
		t.Fatalf("signals = %+v", signals)
	}
	var corrected interactionSignal
	if code := f.send(http.MethodPost, "/api/v1/feedback/interaction-state", map[string]any{"observation_id": kinds["urgency"], "accurate": false}, &corrected); code != http.StatusOK || !corrected.Corrected {
		t.Fatalf("feedback = %d %+v", code, corrected)
	}
	if code := f.send(http.MethodDelete, "/api/v1/insights/interaction-state", nil, nil); code != http.StatusOK {
		t.Fatalf("clear = %d", code)
	}
	if _, err := os.Stat(f.server.interactionSettingsPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("derived state not deleted: %v", err)
	}
	f.get("/api/v1/insights/interaction-state", &signals)
	if len(signals) != 0 {
		t.Fatal("clearing did not turn the feature off")
	}
}

func TestIntegrityReportsHooksMCPAndReconcile(t *testing.T) {
	f := newFixture(t)
	var view integrityView
	f.get("/api/v1/integrity", &view)
	if view.Hooks != "critical" || view.MCP != "warning" {
		t.Fatalf("without hooks = %+v", view)
	}
	os.MkdirAll(filepath.Join(f.server.paths.Root, ".claude"), 0o700)
	os.WriteFile(filepath.Join(f.server.paths.Root, ".claude", "settings.json"), []byte(`{"hooks":{"PreToolUse":[{"hooks":[{"command":"'/bin/acr' 'hook' 'claude' 'pre-tool-use'"}]}]}}`), 0o600)
	os.WriteFile(filepath.Join(f.server.paths.Root, ".mcp.json"), []byte(`{"mcpServers":{"acr":{}}}`), 0o600)
	f.get("/api/v1/integrity", &view)
	if view.Hooks != "warning" {
		t.Fatalf("installed but silent hooks = %s", view.Hooks)
	}
	f.add(eventstore.AppendRequest{Type: "session.started", Actor: eventstore.Actor{Kind: "agent", AgentKind: "claude-code", NativeSessionID: "c1"}})
	f.get("/api/v1/integrity", &view)
	if view.Hooks != "healthy" || view.MCP != "warning" || view.HashChain != "healthy" {
		t.Fatalf("reporting hooks = %+v", view)
	}

	if code := f.send(http.MethodPost, "/api/v1/reconcile/scan", map[string]any{}, nil); code != http.StatusNotImplemented {
		t.Fatalf("scan without workspace = %d", code)
	}
	scanned := false
	f.server.UseWorkspace(Workspace{
		Scan: func(context.Context) error { scanned = true; return nil },
		Resolve: func(_ context.Context, gap, resolution, reason string) (eventstore.Event, error) {
			return eventstore.Event{EventID: gap, Type: "coverage.gap_reconciled"}, nil
		},
	})
	if code := f.send(http.MethodPost, "/api/v1/reconcile/scan", map[string]any{}, nil); code != http.StatusOK || !scanned {
		t.Fatalf("scan = %d, called %v", code, scanned)
	}
	var resolved eventView
	if code := f.send(http.MethodPost, "/api/v1/reconcile/gap-1/resolve", map[string]string{"resolution": "current_state_accepted", "reason": "我改的"}, &resolved); code != http.StatusOK || resolved.ID != "gap-1" {
		t.Fatalf("resolve = %d %+v", code, resolved)
	}
}
