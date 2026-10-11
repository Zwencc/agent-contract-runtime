package console

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/agent-contract-runtime/acr/internal/contract"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/memory"
	"github.com/agent-contract-runtime/acr/internal/registry"
)

// eventDetailView is one event with everything recorded about it.
type eventDetailView struct {
	eventView
	Actor        eventstore.Actor     `json:"actor"`
	Causality    eventstore.Causality `json:"causality"`
	EvidenceRefs []string             `json:"evidenceRefs"`
	Payload      json.RawMessage      `json:"payload"`
	RecordedAt   string               `json:"recordedAt"`
}

func makeEventDetail(event eventstore.Event) eventDetailView {
	return eventDetailView{eventView: makeEventView(event), Actor: event.Actor, Causality: event.Causality, EvidenceRefs: event.EvidenceRefs,
		Payload: event.Payload, RecordedAt: event.RecordedAt.Local().Format("2006-01-02 15:04:05.000")}
}

// limitParameter reads ?limit=, defaulting to and capped at maximumEvents.
func limitParameter(request *http.Request) (int, error) {
	value := request.URL.Query().Get("limit")
	if value == "" {
		return maximumEvents, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > maximumEvents {
		return 0, errors.New("limit must be 1 to " + strconv.Itoa(maximumEvents))
	}
	return limit, nil
}

// pagedEvents serves ?after=<sequence>&limit=&type= as a stable cursor: the
// response holds the events after that sequence in order, and X-Next-After
// names the cursor for the following page when there is one.
func (server *Server) pagedEvents(response http.ResponseWriter, request *http.Request) bool {
	query := request.URL.Query()
	if !query.Has("after") && !query.Has("type") {
		return false
	}
	after, err := strconv.ParseUint(query.Get("after"), 10, 64)
	if err != nil && query.Get("after") != "" {
		writeError(response, http.StatusBadRequest, "invalid_cursor", "after 必须是事件序号")
		return true
	}
	limit, err := limitParameter(request)
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_limit", err.Error())
		return true
	}
	filter := eventstore.Filter{}
	if types := query.Get("type"); types != "" {
		filter.Types = strings.Split(types, ",")
	}
	values, err := server.events.ListMatching(request.Context(), server.config.ProjectID, after, limit+1, filter)
	if err != nil {
		writeInternal(response, err)
		return true
	}
	if len(values) > limit {
		values = values[:limit]
		response.Header().Set("X-Next-After", strconv.FormatUint(values[len(values)-1].Sequence, 10))
	}
	result := make([]eventView, 0, len(values))
	for _, event := range values {
		result = append(result, makeEventView(event))
	}
	writeJSON(response, result)
	return true
}

func (server *Server) sessionDetail(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	sessions, err := server.sessionValues(request.Context())
	if err != nil {
		writeInternal(response, err)
		return
	}
	for _, session := range sessions {
		if session.ID != id && session.NativeID != id {
			continue
		}
		events, err := server.sessionEvents(request.Context(), session.ID)
		if err != nil {
			writeInternal(response, err)
			return
		}
		views := make([]eventView, 0, len(events))
		for _, event := range events {
			views = append(views, makeEventView(event))
		}
		writeJSON(response, map[string]any{"session": session, "events": views})
		return
	}
	writeError(response, http.StatusNotFound, "session_not_found", "未找到该会话")
}

func (server *Server) eventDetail(response http.ResponseWriter, request *http.Request) {
	if request.PathValue("project") != server.config.ProjectID {
		writeError(response, http.StatusNotFound, "project_not_found", "该项目不在当前 Console 中")
		return
	}
	sequence, err := strconv.ParseUint(request.PathValue("sequence"), 10, 64)
	if err != nil || sequence == 0 {
		writeError(response, http.StatusBadRequest, "invalid_sequence", "sequence 必须是正整数")
		return
	}
	values, err := server.events.List(request.Context(), server.config.ProjectID, sequence-1, 1)
	if err != nil {
		writeInternal(response, err)
		return
	}
	if len(values) == 0 || values[0].Sequence != sequence {
		writeError(response, http.StatusNotFound, "event_not_found", "未找到该事件")
		return
	}
	writeJSON(response, makeEventDetail(values[0]))
}

// replay returns a session in project order with full details. An agent
// session and the acr launch that started it are replayed together, as in
// acr replay --session.
func (server *Server) replay(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("session")
	limit, err := limitParameter(request)
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_limit", err.Error())
		return
	}
	ctx := request.Context()
	linked := map[string]bool{id: true}
	for _, filter := range []eventstore.Filter{{NativeSessionID: id}, {ACRSessionID: id}} {
		if err := server.events.Each(ctx, server.config.ProjectID, 0, filter, func(event eventstore.Event) error {
			for _, other := range []string{event.Actor.NativeSessionID, event.Actor.ACRSessionID} {
				if other != "" {
					linked[other] = true
				}
			}
			return nil
		}); err != nil {
			writeInternal(response, err)
			return
		}
	}
	bySequence := map[uint64]eventstore.Event{}
	for session := range linked {
		values, err := server.sessionEvents(ctx, session)
		if err != nil {
			writeInternal(response, err)
			return
		}
		for _, event := range values {
			bySequence[event.Sequence] = event
		}
	}
	// The launch itself: its event ID is the launch ID.
	launches, err := server.eventsOfTypes(ctx, "runtime.launch_started")
	if err != nil {
		writeInternal(response, err)
		return
	}
	for _, launch := range launches {
		if linked[launch.EventID] {
			bySequence[launch.Sequence] = launch
		}
	}
	if len(bySequence) == 0 {
		writeError(response, http.StatusNotFound, "session_not_found", "未找到该会话的事件")
		return
	}
	events := make([]eventstore.Event, 0, len(bySequence))
	for _, event := range bySequence {
		events = append(events, event)
	}
	sort.Slice(events, func(left, right int) bool { return events[left].Sequence < events[right].Sequence })
	truncated := len(events) > limit
	if truncated {
		events = events[len(events)-limit:]
	}
	result := make([]eventDetailView, 0, len(events))
	for _, event := range events {
		result = append(result, makeEventDetail(event))
	}
	writeJSON(response, map[string]any{"session": id, "truncated": truncated, "events": result})
}

func (server *Server) contractDetail(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	ctx := request.Context()
	views, err := server.contractValues(ctx)
	if err != nil {
		writeInternal(response, err)
		return
	}
	var view *contractView
	for index := range views {
		if views[index].ID == id {
			view = &views[index]
		}
	}
	var definition *contract.Contract
	entries, managed, err := registry.Active(ctx, server.events, server.config.ProjectID)
	if err != nil {
		writeInternal(response, err)
		return
	}
	for index := range entries {
		if entries[index].Contract.Metadata.ID == id {
			definition = &entries[index].Contract
		}
	}
	if definition == nil {
		values, err := contract.LoadPath(server.paths.Contracts)
		if err != nil {
			writeInternal(response, err)
			return
		}
		for index := range values {
			if values[index].Metadata.ID == id {
				definition = &values[index]
			}
		}
	}
	if definition == nil {
		writeError(response, http.StatusNotFound, "contract_not_found", "未找到指定契约")
		return
	}
	history := []eventView{}
	changes, err := server.eventsOfTypes(ctx, registry.EventActivated, registry.EventRetired)
	if err != nil {
		writeInternal(response, err)
		return
	}
	for _, event := range changes {
		var payload struct {
			Contract   contract.Contract `json:"contract"`
			ContractID string            `json:"contract_id"`
		}
		if json.Unmarshal(event.Payload, &payload) == nil && (payload.Contract.Metadata.ID == id || payload.ContractID == id) {
			history = append(history, makeEventView(event))
		}
	}
	effects := []eventView{}
	hits, err := server.eventsOfTypes(ctx, contractHitTypes...)
	if err != nil {
		writeInternal(response, err)
		return
	}
	for _, event := range hits {
		if contractFromPayload(payloadMap(event.Payload)) == id {
			effects = append(effects, makeEventView(event))
		}
	}
	if len(effects) > maximumEvents {
		effects = effects[len(effects)-maximumEvents:]
	}
	writeJSON(response, map[string]any{"contract": view, "definition": definition, "enforced": view != nil && (managed || definition.Metadata.Status == contract.StatusActive),
		"history": history, "effects": effects})
}

func (server *Server) memoryDetail(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	value, err := server.memories.Get(request.Context(), server.config.ProjectID, id)
	if errors.Is(err, memory.ErrNotFound) {
		writeError(response, http.StatusNotFound, "memory_not_found", "未找到该记忆")
		return
	}
	if err != nil {
		writeInternal(response, err)
		return
	}
	revisions, err := server.memories.History(request.Context(), server.config.ProjectID, id)
	if err != nil {
		writeInternal(response, err)
		return
	}
	view := makeMemoryView(value)
	if usage, err := server.memoryUsage(request.Context()); err == nil {
		view.UsedBy = usage[id]
	}
	writeJSON(response, map[string]any{"memory": view, "value": value, "revisions": revisions})
}
