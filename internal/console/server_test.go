package console

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/memory"
	"github.com/agent-contract-runtime/acr/internal/project"
)

func testServer(t *testing.T) (*Server, memory.Memory) {
	t.Helper()
	root := t.TempDir()
	config, err := project.NewConfig(root, "console-test")
	if err != nil {
		t.Fatal(err)
	}
	paths, err := project.Initialize(root, config)
	if err != nil {
		t.Fatal(err)
	}
	events, err := eventstore.Open(paths.EventDB)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"prompt": "检查真实 API"})
	if _, err := events.Append(context.Background(), eventstore.AppendRequest{ProjectID: config.ProjectID, WorkspaceID: config.WorkspaceID, OccurredAt: time.Now(), Type: "intent.observed", Actor: eventstore.Actor{Kind: "user", NativeSessionID: "session-1", AgentKind: "codex"}, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if err := events.Close(); err != nil {
		t.Fatal(err)
	}
	memories, err := memory.Open(paths.MemoryDB)
	if err != nil {
		t.Fatal(err)
	}
	value, err := memories.CreateCandidate(context.Background(), memory.CreateRequest{ProjectID: config.ProjectID, WorkspaceID: config.WorkspaceID, Kind: "constraint", Priority: memory.Priority(1), Claim: "保留真实证据", Scope: json.RawMessage(`{"paths":["src/**"]}`), EvidenceRefs: []string{"event:test"}, Source: "test", Confidence: 1})
	if err != nil {
		t.Fatal(err)
	}
	value, err = memories.Activate(context.Background(), config.ProjectID, value.MemoryID, "test activation")
	if err != nil {
		t.Fatal(err)
	}
	if err := memories.Close(); err != nil {
		t.Fatal(err)
	}
	assets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>ACR Console</html>")}, "assets/app.js": &fstest.MapFile{Data: []byte("console.log('acr')")}}
	server, err := New(root, assets)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	return server, value
}

func request(t *testing.T, server *Server, method, path string, body io.Reader, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	value := httptest.NewRequest(method, "http://127.0.0.1:7480"+path, body)
	value.Host = "127.0.0.1:7480"
	for _, cookie := range cookies {
		value.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, value)
	return response
}

func TestRealQueriesAndSPAFallback(t *testing.T) {
	server, _ := testServer(t)
	for _, path := range []string{"/api/v1/project", "/api/v1/events", "/api/v1/sessions", "/api/v1/memories", "/api/v1/integrity", "/sessions"} {
		response := request(t, server, http.MethodGet, path, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s returned %d: %s", path, response.Code, response.Body.String())
		}
	}
	response := request(t, server, http.MethodGet, "/api/v1/project", nil)
	if !bytes.Contains(response.Body.Bytes(), []byte(`"name":"console-test"`)) {
		t.Fatalf("project response does not contain real config: %s", response.Body.String())
	}
	response = request(t, server, http.MethodGet, "/api/v1/events", nil)
	if !bytes.Contains(response.Body.Bytes(), []byte(`"type":"intent.observed"`)) {
		t.Fatalf("event response does not contain stored event: %s", response.Body.String())
	}
}

func TestWritesRequireTokenAndUseOptimisticRevision(t *testing.T) {
	server, value := testServer(t)
	path := "/api/v1/memories/" + value.MemoryID + "/contest"
	withoutToken := request(t, server, http.MethodPost, path, bytes.NewBufferString(`{"reason":"证据冲突","revision":2}`))
	if withoutToken.Code != http.StatusForbidden {
		t.Fatalf("write without token returned %d", withoutToken.Code)
	}
	index := request(t, server, http.MethodGet, "/", nil)
	cookies := index.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "acr_console_token" || !cookies[0].HttpOnly {
		t.Fatalf("index cookies = %#v", cookies)
	}
	accepted := request(t, server, http.MethodPost, path, bytes.NewBufferString(`{"reason":"证据冲突","revision":2}`), cookies[0])
	if accepted.Code != http.StatusOK || !bytes.Contains(accepted.Body.Bytes(), []byte(`"state":"contested"`)) || !bytes.Contains(accepted.Body.Bytes(), []byte(`"revisions":3`)) {
		t.Fatalf("accepted transition returned %d: %s", accepted.Code, accepted.Body.String())
	}
	stale := request(t, server, http.MethodPost, path, bytes.NewBufferString(`{"reason":"重复提交","revision":2}`), cookies[0])
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale transition returned %d: %s", stale.Code, stale.Body.String())
	}
}

func TestRejectsNonLoopbackHostAndCrossOriginWrite(t *testing.T) {
	server, value := testServer(t)
	nonLocal := httptest.NewRequest(http.MethodGet, "http://example.com/api/v1/project", nil)
	nonLocal.Host = "example.com"
	nonLocalResponse := httptest.NewRecorder()
	server.ServeHTTP(nonLocalResponse, nonLocal)
	if nonLocalResponse.Code != http.StatusForbidden {
		t.Fatalf("non-loopback host returned %d", nonLocalResponse.Code)
	}
	index := request(t, server, http.MethodGet, "/", nil)
	cookie := index.Result().Cookies()[0]
	crossOrigin := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:7480/api/v1/memories/"+value.MemoryID+"/contest", bytes.NewBufferString(`{"reason":"x","revision":2}`))
	crossOrigin.Host = "127.0.0.1:7480"
	crossOrigin.Header.Set("Origin", "http://evil.example")
	crossOrigin.AddCookie(cookie)
	crossOriginResponse := httptest.NewRecorder()
	server.ServeHTTP(crossOriginResponse, crossOrigin)
	if crossOriginResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-origin write returned %d", crossOriginResponse.Code)
	}
}

// With more events than the console reads at once, it still shows the newest
// sessions, tells a guarded launch from a native one, keeps a session open
// across turns, and reports a coverage gap however old it is.
func TestConsoleReadsRecentSessionsAndAllGaps(t *testing.T) {
	server, _ := testServer(t)
	ctx := context.Background()
	add := func(requests ...eventstore.AppendRequest) {
		t.Helper()
		for index := range requests {
			requests[index].ProjectID, requests[index].OccurredAt = server.config.ProjectID, time.Now()
			if requests[index].Payload == nil {
				requests[index].Payload = json.RawMessage(`{}`)
			}
		}
		if _, err := server.events.AppendBatch(ctx, requests); err != nil {
			t.Fatal(err)
		}
	}
	add(eventstore.AppendRequest{Type: "coverage.gap_detected", Actor: eventstore.Actor{Kind: "runtime"},
		Payload: json.RawMessage(`{"reason":"old gap","observed_changes":[{"path":"src/old.go","kind":"modified"}]}`)})
	filler := []eventstore.AppendRequest{}
	for index := 0; index < maximumEvents+500; index++ {
		filler = append(filler, eventstore.AppendRequest{Type: "action.completed", Actor: eventstore.Actor{Kind: "agent", AgentKind: "codex", NativeSessionID: "old-session"}})
	}
	add(filler...)
	add(
		eventstore.AppendRequest{EventID: "launch-guarded", Type: "runtime.launch_started", Actor: eventstore.Actor{Kind: "runtime", AgentKind: "claude-code"}, Payload: json.RawMessage(`{"mode":"guarded"}`)},
		eventstore.AppendRequest{Type: "action.proposed", Actor: eventstore.Actor{Kind: "agent", AgentKind: "claude-code", NativeSessionID: "launch-guarded", ACRSessionID: "launch-guarded"}},
		eventstore.AppendRequest{Type: "runtime.launch_ended", Actor: eventstore.Actor{Kind: "runtime", AgentKind: "claude-code", ACRSessionID: "launch-guarded"}},
		eventstore.AppendRequest{EventID: "launch-native", Type: "runtime.launch_started", Actor: eventstore.Actor{Kind: "runtime", AgentKind: "claude-code"}, Payload: json.RawMessage(`{"mode":"native"}`)},
		eventstore.AppendRequest{Type: "intent.observed", Actor: eventstore.Actor{Kind: "user", AgentKind: "claude-code", NativeSessionID: "claude-native", ACRSessionID: "launch-native"}, Payload: json.RawMessage(`{"prompt":"最新的任务"}`)},
		eventstore.AppendRequest{Type: "turn.stopped", Actor: eventstore.Actor{Kind: "agent", AgentKind: "claude-code", NativeSessionID: "claude-native", ACRSessionID: "launch-native"}},
	)

	var sessions []sessionView
	if err := json.Unmarshal(request(t, server, http.MethodGet, "/api/v1/sessions", nil).Body.Bytes(), &sessions); err != nil {
		t.Fatal(err)
	}
	byID := map[string]sessionView{}
	for _, session := range sessions {
		byID[session.ID] = session
	}
	if native := byID["launch-native"]; native.Mode != "native" || native.Guarantee != "L1" || native.Complete || native.Intent != "最新的任务" {
		t.Fatalf("native launch = %+v", native)
	}
	if guarded := byID["launch-guarded"]; guarded.Mode != "guarded" || guarded.Guarantee != "L2" || !guarded.Complete {
		t.Fatalf("guarded launch = %+v", guarded)
	}

	var events []eventView
	json.Unmarshal(request(t, server, http.MethodGet, "/api/v1/events?session_id=claude-native", nil).Body.Bytes(), &events)
	if len(events) != 2 || events[0].Type != "intent.observed" || events[1].Type != "turn.stopped" {
		t.Fatalf("session events = %+v", events)
	}
	json.Unmarshal(request(t, server, http.MethodGet, "/api/v1/events", nil).Body.Bytes(), &events)
	if len(events) != maximumEvents || events[len(events)-1].Type != "turn.stopped" {
		t.Fatalf("recent events: %d, last %+v", len(events), events[len(events)-1])
	}

	integrity := request(t, server, http.MethodGet, "/api/v1/integrity", nil).Body.String()
	if !strings.Contains(integrity, "src/old.go") {
		t.Fatalf("an old gap is missing from integrity: %s", integrity)
	}
}
