// Package console serves the project-local ACR Console and its JSON API.
package console

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/agent-contract-runtime/acr/internal/contract"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/memory"
	"github.com/agent-contract-runtime/acr/internal/project"
	"github.com/agent-contract-runtime/acr/internal/projector"
	"github.com/agent-contract-runtime/acr/internal/registry"
)

const maximumEvents = 5000

type Server struct {
	config      project.Config
	paths       project.Paths
	events      *eventstore.Store
	memories    *memory.Store
	projections *projector.Store
	assets      fs.FS
	token       string
	handler     http.Handler
	workspace   Workspace
	verified    verifiedHead
}

func New(root string, assets fs.FS) (*Server, error) {
	config, paths, err := project.Load(root)
	if err != nil {
		return nil, err
	}
	events, err := eventstore.Open(paths.EventDB)
	if err != nil {
		return nil, err
	}
	memories, err := memory.Open(paths.MemoryDB)
	if err != nil {
		events.Close()
		return nil, err
	}
	projections, err := projector.Open(paths.ProjectionDB)
	if err != nil {
		memories.Close()
		events.Close()
		return nil, err
	}
	token, err := randomToken()
	if err != nil {
		projections.Close()
		memories.Close()
		events.Close()
		return nil, err
	}
	server := &Server{config: config, paths: paths, events: events, memories: memories, projections: projections, assets: assets, token: token}
	server.handler = server.routes()
	return server, nil
}

func (server *Server) Close() error {
	return errors.Join(server.projections.Close(), server.memories.Close(), server.events.Close())
}

func (server *Server) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	server.handler.ServeHTTP(response, request)
}

func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate console token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (server *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/project", server.project)
	mux.HandleFunc("GET /api/v1/overview", server.overview)
	mux.HandleFunc("GET /api/v1/sessions", server.sessions)
	mux.HandleFunc("GET /api/v1/sessions/{id}", server.sessionDetail)
	mux.HandleFunc("GET /api/v1/events", server.eventsList)
	mux.HandleFunc("GET /api/v1/events/{project}/{sequence}", server.eventDetail)
	mux.HandleFunc("GET /api/v1/replay/{session}", server.replay)
	mux.HandleFunc("GET /api/v1/contracts", server.contracts)
	mux.HandleFunc("GET /api/v1/contracts/{id}", server.contractDetail)
	mux.HandleFunc("POST /api/v1/contracts/drafts", server.createDraft)
	mux.HandleFunc("POST /api/v1/contracts/{id}/simulate", server.simulateContract)
	mux.HandleFunc("POST /api/v1/contracts/{id}/approve", server.activateContract)
	mux.HandleFunc("POST /api/v1/contracts/{id}/activate", server.activateContract)
	mux.HandleFunc("POST /api/v1/contracts/{id}/deactivate", server.deactivateContract)
	mux.HandleFunc("GET /api/v1/memories", server.memoryList)
	mux.HandleFunc("GET /api/v1/memories/{id}", server.memoryDetail)
	mux.HandleFunc("POST /api/v1/memories/{id}/correct", server.correctMemory)
	mux.HandleFunc("POST /api/v1/memories/{id}/{action}", server.transitionMemory)
	mux.HandleFunc("POST /api/v1/memories/compact/preview", server.compactMemories)
	mux.HandleFunc("POST /api/v1/memories/compact/apply", server.applyCompaction)
	mux.HandleFunc("POST /api/v1/reconcile/scan", server.reconcileScan)
	mux.HandleFunc("POST /api/v1/reconcile/{id}/resolve", server.reconcileResolve)
	mux.HandleFunc("GET /api/v1/graph", server.graph)
	mux.HandleFunc("GET /api/v1/insights/agents", server.agentInsights)
	mux.HandleFunc("GET /api/v1/insights/failures", server.failures)
	mux.HandleFunc("GET /api/v1/insights/reliance", server.reliance)
	mux.HandleFunc("GET /api/v1/insights/interaction-state", server.interactionState)
	mux.HandleFunc("POST /api/v1/feedback/interaction-state", server.interactionFeedback)
	mux.HandleFunc("GET /api/v1/settings/interaction-state", server.interactionSettings)
	mux.HandleFunc("PUT /api/v1/settings/interaction-state", server.updateInteractionSettings)
	mux.HandleFunc("DELETE /api/v1/insights/interaction-state", server.clearInteractionState)
	mux.HandleFunc("GET /api/v1/integrity", server.integrity)
	mux.HandleFunc("GET /api/v1/stream", server.stream)
	mux.HandleFunc("GET /", server.static)
	return server.security(mux)
}

func (server *Server) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		if !loopbackHost(request.Host) {
			writeError(response, http.StatusForbidden, "invalid_host", "ACR Console 只接受本机访问")
			return
		}
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			if !sameOrigin(request) {
				writeError(response, http.StatusForbidden, "invalid_origin", "请求来源与 ACR Console 不一致")
				return
			}
			cookie, err := request.Cookie("acr_console_token")
			if err != nil || cookie.Value != server.token {
				writeError(response, http.StatusForbidden, "missing_token", "缺少有效的本地 Console 启动令牌")
				return
			}
		}
		next.ServeHTTP(response, request)
	})
}

func loopbackHost(hostport string) bool {
	host := hostport
	if parsed, _, err := net.SplitHostPort(hostport); err == nil {
		host = parsed
	}
	host = strings.Trim(host, "[]")
	return strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func sameOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && strings.EqualFold(parsed.Host, request.Host)
}

func (server *Server) static(response http.ResponseWriter, request *http.Request) {
	path := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(request.URL.Path)), "/")
	if path == "." || path == "" {
		path = "index.html"
	}
	if _, err := fs.Stat(server.assets, path); err != nil {
		path = "index.html"
	}
	data, err := fs.ReadFile(server.assets, path)
	if err != nil {
		writeError(response, http.StatusNotFound, "asset_not_found", "Console 资源不存在")
		return
	}
	http.SetCookie(response, &http.Cookie{Name: "acr_console_token", Value: server.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	if strings.HasSuffix(path, ".html") {
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		response.Header().Set("Cache-Control", "no-store")
	} else if strings.HasSuffix(path, ".js") {
		response.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	} else if strings.HasSuffix(path, ".css") {
		response.Header().Set("Content-Type", "text/css; charset=utf-8")
		response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	response.Write(data)
}

func (server *Server) project(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, projectView{Name: server.config.Name, Workspace: server.paths.Root, ProjectID: server.config.ProjectID, WorkspaceID: server.config.WorkspaceID})
}

// allEvents returns the most recent maximumEvents events, oldest first.
func (server *Server) allEvents(ctx context.Context) ([]eventstore.Event, error) {
	return server.events.Latest(ctx, server.config.ProjectID, maximumEvents, eventstore.Filter{})
}

// eventsOfTypes returns every event of the given types, whatever its age,
// through the type index.
func (server *Server) eventsOfTypes(ctx context.Context, types ...string) ([]eventstore.Event, error) {
	values := []eventstore.Event{}
	err := server.events.Each(ctx, server.config.ProjectID, 0, eventstore.Filter{Types: types}, func(event eventstore.Event) error {
		values = append(values, event)
		return nil
	})
	return values, err
}

// sessionEvents returns the latest events of a session, whether id is an
// agent session or an acr launch.
func (server *Server) sessionEvents(ctx context.Context, id string) ([]eventstore.Event, error) {
	bySequence := map[uint64]eventstore.Event{}
	for _, filter := range []eventstore.Filter{{NativeSessionID: id}, {ACRSessionID: id}} {
		values, err := server.events.Latest(ctx, server.config.ProjectID, maximumEvents, filter)
		if err != nil {
			return nil, err
		}
		for _, event := range values {
			bySequence[event.Sequence] = event
		}
	}
	result := make([]eventstore.Event, 0, len(bySequence))
	for _, event := range bySequence {
		result = append(result, event)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Sequence < result[right].Sequence })
	if len(result) > maximumEvents {
		result = result[len(result)-maximumEvents:]
	}
	return result, nil
}

func (server *Server) eventsList(response http.ResponseWriter, request *http.Request) {
	if server.pagedEvents(response, request) {
		return
	}
	sessionID := request.URL.Query().Get("session_id")
	values, err := server.allEvents(request.Context())
	if sessionID != "" {
		values, err = server.sessionEvents(request.Context(), sessionID)
	}
	if err != nil {
		writeInternal(response, err)
		return
	}
	result := []eventView{}
	for _, event := range values {
		result = append(result, makeEventView(event))
	}
	writeJSON(response, result)
}

func makeEventView(event eventstore.Event) eventView {
	payload := payloadMap(event.Payload)
	agent := string(event.Actor.AgentKind)
	if agent == "" {
		agent = event.Actor.Kind
	}
	status := "unknown"
	switch {
	case strings.HasSuffix(event.Type, ".failed") || strings.HasSuffix(event.Type, ".denied") || event.Type == "coverage.gap_detected":
		status = "critical"
	case strings.HasPrefix(event.Type, "repair.") || strings.HasSuffix(event.Type, ".rewritten") || strings.HasSuffix(event.Type, ".asked"):
		status = "warning"
	case strings.HasSuffix(event.Type, ".passed") || strings.HasSuffix(event.Type, ".completed") || strings.HasSuffix(event.Type, ".verified") || strings.HasSuffix(event.Type, ".activated"):
		status = "healthy"
	}
	detail := firstString(payload, "reason", "error", "prompt", "instruction")
	if detail == "" {
		detail = "已记录不可变事件证据"
	}
	return eventView{Sequence: event.Sequence, ID: event.EventID, Type: event.Type, Time: event.OccurredAt.Local().Format("15:04:05"), Agent: agent, Status: status, Summary: eventSummary(event.Type), Detail: detail, Contract: contractFromPayload(payload), Command: commandFromPayload(payload), Resources: resourcesFromPayload(payload), PreviousHash: event.PreviousHash, Hash: event.EventHash}
}

func eventSummary(kind string) string {
	parts := strings.Split(kind, ".")
	for index := range parts {
		parts[index] = strings.ReplaceAll(parts[index], "_", " ")
	}
	return strings.Join(parts, " · ")
}

func payloadMap(data json.RawMessage) map[string]any {
	value := map[string]any{}
	json.Unmarshal(data, &value)
	return value
}

func firstString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := value[key].(string); ok && text != "" {
			return text
		}
	}
	return ""
}

func nestedMap(value map[string]any, keys ...string) map[string]any {
	current := value
	for _, key := range keys {
		next, ok := current[key].(map[string]any)
		if !ok {
			return nil
		}
		current = next
	}
	return current
}

func commandFromPayload(payload map[string]any) string {
	for _, input := range []map[string]any{nestedMap(payload, "tool", "input"), nestedMap(payload, "tool_input")} {
		if input != nil {
			if command, ok := input["command"].(string); ok {
				return command
			}
		}
	}
	return firstString(payload, "command")
}

func contractFromPayload(payload map[string]any) string {
	if id := firstString(payload, "contract_id"); id != "" {
		return id
	}
	if matches, ok := payload["matches"].([]any); ok && len(matches) > 0 {
		if match, ok := matches[0].(map[string]any); ok {
			return firstString(match, "contract_id")
		}
	}
	return ""
}

func resourcesFromPayload(payload map[string]any) []string {
	set := map[string]bool{}
	for _, key := range []string{"resources", "observed_changes"} {
		for _, item := range anySlice(payload[key]) {
			switch value := item.(type) {
			case string:
				set[value] = true
			case map[string]any:
				if path := firstString(value, "path"); path != "" {
					set[path] = true
				}
			}
		}
	}
	result := make([]string, 0, len(set))
	for path := range set {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

func anySlice(value any) []any {
	result, _ := value.([]any)
	return result
}

func (server *Server) sessionValues(ctx context.Context) ([]sessionView, error) {
	events, err := server.allEvents(ctx)
	if err != nil {
		return nil, err
	}
	// Every event of an acr launch carries the launch ID, whatever its mode;
	// the launch itself records whether it was guarded.
	launches, err := server.eventsOfTypes(ctx, "runtime.launch_started")
	if err != nil {
		return nil, err
	}
	guardedLaunch := map[string]bool{}
	for _, launch := range launches {
		guardedLaunch[launch.EventID] = firstString(payloadMap(launch.Payload), "mode") == "guarded"
	}
	type sessionState struct {
		view sessionView
		start time.Time
		last time.Time
		files map[string]bool
	}
	states := map[string]*sessionState{}
	for _, event := range events {
		if event.Actor.NativeSessionID == "" && event.Actor.ACRSessionID == "" {
			continue
		}
		key := event.Actor.ACRSessionID
		if key == "" {
			key = event.Actor.NativeSessionID
		}
		state := states[key]
		if state == nil {
			mode, guarantee := "native", "L1"
			if guardedLaunch[event.Actor.ACRSessionID] {
				mode, guarantee = "guarded", "L2"
			}
			state = &sessionState{view: sessionView{ID: key, NativeID: event.Actor.NativeSessionID, Agent: string(event.Actor.AgentKind), Guarantee: guarantee, Mode: mode, Status: "active", Intent: "尚未记录用户意图", Files: []string{}}, start: event.OccurredAt, files: map[string]bool{}}
			states[key] = state
		}
		if event.OccurredAt.Before(state.start) {
			state.start = event.OccurredAt
		}
		if event.OccurredAt.After(state.last) {
			state.last = event.OccurredAt
		}
		payload := payloadMap(event.Payload)
		switch event.Type {
		case "intent.observed":
			if prompt := firstString(payload, "prompt"); prompt != "" {
				state.view.Intent = prompt
			}
		case "action.proposed":
			state.view.Actions++
		case "action.denied":
			state.view.Denied++
		case "repair.completed":
			state.view.Repaired++
		case "session.ended", "runtime.launch_ended":
			// turn.stopped only ends a turn; the session goes on.
			state.view.Complete = true
			state.view.Status = "completed"
		}
		for _, path := range resourcesFromPayload(payload) {
			state.files[path] = true
		}
	}
	result := make([]sessionView, 0, len(states))
	for _, state := range states {
		if state.view.Agent != "claude-code" && state.view.Agent != "codex" {
			continue
		}
		state.view.StartedAt = state.start.Local().Format("2006-01-02 15:04")
		end := state.last
		if !state.view.Complete {
			end = time.Now()
		}
		state.view.Duration = formatDuration(end.Sub(state.start))
		for path := range state.files {
			state.view.Files = append(state.view.Files, path)
		}
		sort.Strings(state.view.Files)
		result = append(result, state.view)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].StartedAt > result[right].StartedAt })
	for index := 0; index+1 < len(result); index++ {
		result[index].PreviousAgent = result[index+1].Agent
	}
	return result, nil
}

func formatDuration(value time.Duration) string {
	if value < 0 {
		value = 0
	}
	return fmt.Sprintf("%02d:%02d", int(value.Minutes()), int(value.Seconds())%60)
}

func (server *Server) sessions(response http.ResponseWriter, request *http.Request) {
	values, err := server.sessionValues(request.Context())
	if err != nil {
		writeInternal(response, err)
		return
	}
	writeJSON(response, values)
}

func (server *Server) overview(response http.ResponseWriter, request *http.Request) {
	events, err := server.allEvents(request.Context())
	if err != nil {
		writeInternal(response, err)
		return
	}
	sessions, err := server.sessionValues(request.Context())
	if err != nil {
		writeInternal(response, err)
		return
	}
	contracts, err := server.contractValues(request.Context())
	if err != nil {
		writeInternal(response, err)
		return
	}
	memories, _ := server.memories.List(request.Context(), server.config.ProjectID, memory.MaximumRetrieveLimit)
	gaps, err := server.unresolvedGaps(request.Context())
	if err != nil {
		writeInternal(response, err)
		return
	}
	passed, repaired, denied := 0, 0, 0
	agents := map[string]int{"claude-code": 0, "codex": 0}
	for _, event := range events {
		switch event.Type {
		case "action.passed", "action.completed":
			passed++
		case "repair.completed":
			repaired++
		case "action.denied":
			denied++
		}
		if _, ok := agents[string(event.Actor.AgentKind)]; ok && event.Type == "action.proposed" {
			agents[string(event.Actor.AgentKind)]++
		}
	}
	mode, agent, guarantee := "native", "claude-code", "L1"
	if len(sessions) > 0 {
		mode, agent, guarantee = sessions[0].Mode, sessions[0].Agent, sessions[0].Guarantee
	}
	tone := "healthy"
	if len(gaps) > 0 {
		tone = "warning"
	}
	attention := []attentionView{}
	for _, gap := range gaps {
		attention = append(attention, attentionView{ID: gap.EventID, Severity: "high", Title: "存在未解决的工作区覆盖缺口", Detail: firstString(payloadMap(gap.Payload), "reason"), Route: "/integrity"})
	}
	writeJSON(response, overviewView{Guarantee: guarantee, Mode: mode, Agent: agent,
		Metrics: []metricView{{Label: "Active contracts", Value: len(contracts), Delta: "实时", Tone: "healthy", Detail: "来自契约注册表"}, {Label: "Repairs succeeded", Value: repaired, Delta: strconv.Itoa(repaired), Tone: "healthy", Detail: "repair.completed"}, {Label: "Memory health", Value: len(memories), Delta: "真实记录", Tone: "healthy", Detail: "当前记忆版本"}, {Label: "Coverage gaps", Value: len(gaps), Delta: "未解决", Tone: tone, Detail: "覆盖对账结果"}},
		Attention: attention, Activity: activity(events), AgentShare: []agentShareView{{Name: "claude-code", Value: agents["claude-code"]}, {Name: "codex", Value: agents["codex"]}}})
	_ = passed
	_ = denied
}

func activity(events []eventstore.Event) []activityView {
	now := time.Now()
	result := make([]activityView, 5)
	for index := range result {
		result[index].Time = now.Add(time.Duration(index-4) * time.Hour).Format("15:00")
	}
	for _, event := range events {
		difference := int(now.Sub(event.OccurredAt).Hours())
		if difference < 0 || difference > 4 {
			continue
		}
		bucket := 4 - difference
		switch event.Type {
		case "action.passed", "action.completed":
			result[bucket].Passed++
		case "repair.completed":
			result[bucket].Repaired++
		case "action.denied":
			result[bucket].Denied++
		}
	}
	return result
}

func (server *Server) unresolvedGaps(ctx context.Context) ([]eventstore.Event, error) {
	events, err := server.eventsOfTypes(ctx, "coverage.gap_detected", "coverage.gap_reconciled")
	if err != nil {
		return nil, err
	}
	open := map[string]eventstore.Event{}
	order := []string{}
	for _, event := range events {
		switch event.Type {
		case "coverage.gap_detected":
			open[event.EventID] = event
			order = append(order, event.EventID)
		case "coverage.gap_reconciled":
			delete(open, firstString(payloadMap(event.Payload), "gap_event_id"))
		}
	}
	result := []eventstore.Event{}
	for _, id := range order {
		if event, ok := open[id]; ok {
			result = append(result, event)
		}
	}
	return result, nil
}

func (server *Server) contractValues(ctx context.Context) ([]contractView, error) {
	active, managed, err := registry.Active(ctx, server.events, server.config.ProjectID)
	if err != nil {
		return nil, err
	}
	events, err := server.eventsOfTypes(ctx, contractHitTypes...)
	if err != nil {
		return nil, err
	}
	values := []contract.Contract{}
	if managed {
		for _, entry := range active {
			values = append(values, entry.Contract)
		}
	} else {
		values, err = contract.LoadPath(server.paths.Contracts)
		if err != nil {
			return nil, err
		}
	}
	result := make([]contractView, 0, len(values))
	for _, value := range values {
		hits, repairs, last := contractStats(events, value.Metadata.ID)
		applicability, _ := json.Marshal(value.Applicability)
		scope, _ := json.Marshal(value.Scope)
		verifier := "确定性契约评估"
		if value.Verifiers != nil {
			encoded, _ := json.Marshal(value.Verifiers)
			verifier = string(encoded)
		}
		result = append(result, contractView{ID: value.Metadata.ID, Name: value.Metadata.Title, Description: value.Behavior.Instruction, Version: value.Metadata.Version, Priority: fmt.Sprintf("P%d", value.Metadata.Priority), State: string(value.Metadata.Status), Scope: string(scope), Hits: hits, Repairs: repairs, LastHit: last, FailureMode: string(value.FailureMode), Applicability: string(applicability), Instruction: value.Behavior.Instruction, Verifier: verifier})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

// contractHitTypes are the events that count as a contract taking effect.
var contractHitTypes = []string{"action.denied", "action.rewritten", "action.asked", "repair.completed"}

func contractStats(events []eventstore.Event, id string) (int, int, string) {
	hits, repairs, last := 0, 0, "从未命中"
	for _, event := range events {
		payload := payloadMap(event.Payload)
		if contractFromPayload(payload) != id {
			continue
		}
		switch event.Type {
		case "action.denied", "action.rewritten", "action.asked":
			hits++
			last = event.OccurredAt.Local().Format("2006-01-02 15:04")
		case "repair.completed":
			repairs++
		}
	}
	return hits, repairs, last
}

func (server *Server) contracts(response http.ResponseWriter, request *http.Request) {
	values, err := server.contractValues(request.Context())
	if err != nil {
		writeInternal(response, err)
		return
	}
	writeJSON(response, values)
}

func makeMemoryView(value memory.Memory) memoryView {
	scope := string(value.Scope)
	return memoryView{ID: value.MemoryID, Claim: value.Claim, Kind: value.Kind, State: string(value.Status), Priority: int(value.Priority), Confidence: value.Confidence, Scope: scope, EvidenceCount: len(value.EvidenceRefs), Revisions: value.Revision, UpdatedAt: value.UpdatedAt.Local().Format("2006-01-02 15:04"), UsedBy: []string{}}
}

func (server *Server) memoryList(response http.ResponseWriter, request *http.Request) {
	values, err := server.memories.List(request.Context(), server.config.ProjectID, memory.MaximumRetrieveLimit)
	if err != nil {
		writeInternal(response, err)
		return
	}
	usage, err := server.memoryUsage(request.Context())
	if err != nil {
		writeInternal(response, err)
		return
	}
	result := make([]memoryView, 0, len(values))
	for _, value := range values {
		view := makeMemoryView(value)
		if agents := usage[value.MemoryID]; agents != nil {
			view.UsedBy = agents
		}
		result = append(result, view)
	}
	writeJSON(response, result)
}

func (server *Server) transitionMemory(response http.ResponseWriter, request *http.Request) {
	id, action := request.PathValue("id"), request.PathValue("action")
	var input struct {
		Reason   string `json:"reason"`
		Revision uint64 `json:"revision"`
	}
	if err := decodeJSON(request.Body, &input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	current, err := server.memories.Get(request.Context(), server.config.ProjectID, id)
	if err != nil {
		writeError(response, http.StatusNotFound, "memory_not_found", err.Error())
		return
	}
	if input.Revision == 0 {
		input.Revision = current.Revision
	}
	if strings.TrimSpace(input.Reason) == "" {
		input.Reason = "ACR Console 用户操作"
	}
	var updated memory.Memory
	switch action {
	case "confirm":
		updated, err = server.memories.ConfirmAtRevision(request.Context(), server.config.ProjectID, id, input.Revision, current.EvidenceRefs)
	case "contest":
		updated, err = server.memories.ContestAtRevision(request.Context(), server.config.ProjectID, id, input.Revision, input.Reason)
	case "retract":
		updated, err = server.memories.RetractAtRevision(request.Context(), server.config.ProjectID, id, input.Revision, input.Reason)
	case "archive":
		updated, err = server.memories.ArchiveAtRevision(request.Context(), server.config.ProjectID, id, input.Revision, input.Reason)
	default:
		writeError(response, http.StatusNotFound, "unsupported_action", "不支持该记忆状态操作")
		return
	}
	if err != nil {
		writeError(response, http.StatusConflict, "memory_transition_failed", err.Error())
		return
	}
	writeJSON(response, makeMemoryView(updated))
}

func (server *Server) graph(response http.ResponseWriter, request *http.Request) {
	if _, err := server.projections.Sync(request.Context(), server.events, server.config.ProjectID, server.paths.Root); err != nil {
		writeInternal(response, err)
		return
	}
	nodes, err := server.projections.Nodes(request.Context(), server.config.ProjectID)
	if err != nil {
		writeInternal(response, err)
		return
	}
	edges, err := server.projections.Edges(request.Context(), server.config.ProjectID)
	if err != nil {
		writeInternal(response, err)
		return
	}
	result := graphView{Nodes: []graphNodeView{}, Edges: []graphEdgeView{}}
	for index, node := range nodes {
		kind := "module"
		switch node.Type {
		case projector.NodeFailure:
			kind = "failure"
		case projector.NodeVerification:
			kind = "verification"
		}
		result.Nodes = append(result.Nodes, graphNodeView{ID: node.ID, Label: node.Key, Kind: kind, X: float64(index%4) * 220, Y: float64(index/4) * 130, Detail: string(node.Attributes)})
	}
	for index, edge := range edges {
		result.Edges = append(result.Edges, graphEdgeView{ID: fmt.Sprintf("edge-%d", index), Source: edge.FromID, Target: edge.ToID, Label: strings.ToLower(edge.Type)})
	}
	writeJSON(response, result)
}

func (server *Server) integrity(response http.ResponseWriter, request *http.Request) {
	head, headErr := server.events.Head(request.Context(), server.config.ProjectID)
	chainStatus, chainDetail := "healthy", "事件哈希链已验证"
	verifiedAt, err := server.verifyChain(request.Context())
	if err != nil {
		chainStatus, chainDetail = "critical", err.Error()
	}
	memoryStatus, memoryDetail := "healthy", "记忆版本链已验证"
	if err := server.memories.Verify(request.Context(), server.config.ProjectID); err != nil && !errors.Is(err, memory.ErrNotFound) {
		memoryStatus, memoryDetail = "critical", err.Error()
	}
	gaps, err := server.unresolvedGaps(request.Context())
	if err != nil {
		writeInternal(response, err)
		return
	}
	gapViews := []gapView{}
	for _, gap := range gaps {
		paths := resourcesFromPayload(payloadMap(gap.Payload))
		if len(paths) == 0 {
			paths = []string{"未知路径"}
		}
		for _, path := range paths {
			gapViews = append(gapViews, gapView{ID: gap.EventID, Path: path, DetectedAt: gap.OccurredAt.Local().Format("2006-01-02 15:04"), Resolution: "未解决"})
		}
	}
	coverageStatus := "healthy"
	if len(gapViews) > 0 {
		coverageStatus = "warning"
	}
	lastSequence, lastVerified := uint64(0), "尚无事件"
	if headErr == nil {
		lastSequence, lastVerified = head.Sequence, verifiedAt.Local().Format("2006-01-02 15:04:05")
	}
	hooks, hooksDetail := server.hookHealth(request.Context())
	mcp, mcpDetail := server.mcpHealth()
	writeJSON(response, integrityView{HashChain: chainStatus, Coverage: coverageStatus, Hooks: hooks, MCP: mcp, LastSequence: lastSequence, LastVerified: lastVerified, Gaps: gapViews, Checks: []integrityCheck{{Name: "事件哈希链", Status: chainStatus, Detail: chainDetail}, {Name: "记忆版本链", Status: memoryStatus, Detail: memoryDetail}, {Name: "工作区覆盖对账", Status: coverageStatus, Detail: fmt.Sprintf("%d 个未解决缺口", len(gapViews))}, {Name: "Agent Hook", Status: hooks, Detail: hooksDetail}, {Name: "ACR 工具（MCP）", Status: mcp, Detail: mcpDetail}}})
}

func (server *Server) stream(response http.ResponseWriter, request *http.Request) {
	flusher, ok := response.(http.Flusher)
	if !ok {
		writeError(response, http.StatusNotImplemented, "stream_unsupported", "当前 HTTP 服务不支持 SSE")
		return
	}
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-cache")
	response.Header().Set("X-Accel-Buffering", "no")
	last := request.Header.Get("Last-Event-ID")
	if last == "" {
		last = "0"
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		head, err := server.events.Head(request.Context(), server.config.ProjectID)
		if err == nil && strconv.FormatUint(head.Sequence, 10) != last {
			last = strconv.FormatUint(head.Sequence, 10)
			fmt.Fprintf(response, "id: %s\nevent: project\ndata: {\"sequence\":%s}\n\n", last, last)
			flusher.Flush()
		}
		select {
		case <-request.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func decodeJSON(reader io.Reader, target any) error {
	decoder := json.NewDecoder(io.LimitReader(reader, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("解析 JSON 请求: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("JSON 请求只能包含一个值")
	}
	return nil
}

func writeJSON(response http.ResponseWriter, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(response).Encode(value); err != nil {
		return
	}
}

func writeInternal(response http.ResponseWriter, err error) {
	writeError(response, http.StatusInternalServerError, "internal_error", err.Error())
}

func writeError(response http.ResponseWriter, status int, code, message string) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	json.NewEncoder(response).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func EnsureAssets(assets fs.FS) error {
	if assets == nil {
		return errors.New("console assets are required")
	}
	if _, err := fs.Stat(assets, "index.html"); err != nil {
		return fmt.Errorf("inspect console assets: %w", err)
	}
	return nil
}

func Exists(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".acr", "project.json"))
	return err == nil
}
