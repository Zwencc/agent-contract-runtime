package console

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/agent-contract-runtime/acr/internal/contract"
	"github.com/agent-contract-runtime/acr/internal/evaluator"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/memory"
	"github.com/agent-contract-runtime/acr/internal/protocol"
	"github.com/agent-contract-runtime/acr/internal/registry"
)

// Workspace runs the workspace coverage reconciliation, which the acr
// command owns; acr ui provides it.
type Workspace struct {
	// Scan compares the workspace with its baseline and records any gap.
	Scan func(ctx context.Context) error
	// Resolve records a person's resolution of an open coverage gap.
	Resolve func(ctx context.Context, gapEventID, resolution, reason string) (eventstore.Event, error)
}

// UseWorkspace enables the reconcile commands.
func (server *Server) UseWorkspace(workspace Workspace) { server.workspace = workspace }

// simulationWindow is how many recorded actions a simulation replays.
const simulationWindow = 2000

type simulationView struct {
	Evaluated int              `json:"evaluated"`
	Matched   int              `json:"matched"`
	Blocked   int              `json:"blocked"`
	Repaired  int              `json:"repaired"`
	Asked     int              `json:"asked"`
	Samples   []simulationCase `json:"samples"`
}

type simulationCase struct {
	Sequence uint64 `json:"sequence"`
	Agent    string `json:"agent"`
	Tool     string `json:"tool"`
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// simulateContract replays the recently recorded actions against one
// contract as if it were the only active one: the contract with that ID, or
// a draft sent as {"contract": {...}}. Nothing is executed or recorded.
func (server *Server) simulateContract(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Contract json.RawMessage `json:"contract"`
	}
	if request.ContentLength != 0 {
		if err := decodeJSON(request.Body, &input); err != nil {
			writeError(response, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	value, err := server.findContract(request.Context(), request.PathValue("id"), input.Contract)
	if err != nil {
		writeError(response, http.StatusNotFound, "contract_not_found", err.Error())
		return
	}
	value.Metadata.Status = contract.StatusActive
	engine, err := evaluator.New([]contract.Contract{value})
	if err != nil {
		writeError(response, http.StatusUnprocessableEntity, "invalid_contract", err.Error())
		return
	}
	proposals, err := server.events.Latest(request.Context(), server.config.ProjectID, simulationWindow, eventstore.Filter{Types: []string{"action.proposed"}})
	if err != nil {
		writeInternal(response, err)
		return
	}
	result := simulationView{Samples: []simulationCase{}}
	for _, event := range proposals {
		var action protocol.Action
		if json.Unmarshal(event.Payload, &action) != nil || action.Tool.Name == "" {
			continue
		}
		decision, err := engine.Evaluate(action)
		if err != nil {
			continue
		}
		result.Evaluated++
		if decision.Kind == protocol.DecisionPass && len(decision.Matches) == 0 {
			continue
		}
		result.Matched++
		switch decision.Kind {
		case protocol.DecisionDeny:
			result.Blocked++
		case protocol.DecisionAsk:
			result.Asked++
		case protocol.DecisionRewrite:
			result.Repaired++
		}
		if len(result.Samples) < 20 {
			result.Samples = append(result.Samples, simulationCase{Sequence: event.Sequence, Agent: string(action.Agent.Kind), Tool: action.Tool.Name, Decision: string(decision.Kind), Reason: decision.Reason})
		}
	}
	writeJSON(response, result)
}

// findContract returns the draft in raw when given, otherwise the enforced or
// draft contract with id.
func (server *Server) findContract(ctx context.Context, id string, raw json.RawMessage) (contract.Contract, error) {
	if len(raw) != 0 && string(raw) != "null" {
		return contract.DecodeBytes(raw)
	}
	entries, _, err := registry.Active(ctx, server.events, server.config.ProjectID)
	if err != nil {
		return contract.Contract{}, err
	}
	for _, entry := range entries {
		if entry.Contract.Metadata.ID == id {
			return entry.Contract, nil
		}
	}
	values, err := contract.LoadPath(server.paths.Contracts)
	if err != nil {
		return contract.Contract{}, err
	}
	for _, value := range values {
		if value.Metadata.ID == id {
			return value, nil
		}
	}
	return contract.Contract{}, errors.New("未找到指定契约")
}

// createDraft validates a contract and saves it as a draft in the contract
// directory. A draft is never enforced until someone activates it.
func (server *Server) createDraft(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Contract json.RawMessage `json:"contract"`
	}
	if err := decodeJSON(request.Body, &input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	value, err := contract.DecodeBytes(input.Contract)
	if err != nil {
		writeError(response, http.StatusUnprocessableEntity, "invalid_contract", err.Error())
		return
	}
	if strings.HasPrefix(value.Metadata.ID, "acr-builtin-") {
		writeError(response, http.StatusConflict, "builtin_contract", "内置契约不能被替换")
		return
	}
	value.Metadata.Status = contract.StatusDraft
	if err := value.Validate(); err != nil {
		writeError(response, http.StatusUnprocessableEntity, "invalid_contract", err.Error())
		return
	}
	path := filepath.Join(server.paths.Contracts, value.Metadata.ID+".json")
	if entries, _, err := registry.Active(request.Context(), server.events, server.config.ProjectID); err == nil {
		for _, entry := range entries {
			if entry.Contract.Metadata.ID == value.Metadata.ID {
				// The enforced copy keeps its file; the draft waits beside it.
				path = filepath.Join(server.paths.Contracts, value.Metadata.ID+".draft.json")
			}
		}
	}
	encoded, _ := json.MarshalIndent(value, "", "  ")
	if err := os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
		writeInternal(response, err)
		return
	}
	writeJSON(response, map[string]any{"contract": value, "path": path, "state": "draft"})
}

// activateContract puts a contract from the contract directory in force. In
// this version approving a contract and activating it are the same step:
// the registry records who did it and when.
func (server *Server) activateContract(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	values, err := contract.LoadPath(server.paths.Contracts)
	if err != nil {
		writeInternal(response, err)
		return
	}
	for _, value := range values {
		if value.Metadata.ID != id {
			continue
		}
		entry, err := registry.Activate(request.Context(), server.events, server.config.ProjectID, server.config.WorkspaceID, server.paths.Contracts, value, registry.Source{Kind: registry.SourceConsole}, time.Now())
		if err != nil {
			writeError(response, http.StatusConflict, "contract_activation_failed", err.Error())
			return
		}
		views, _ := server.contractValues(request.Context())
		for _, view := range views {
			if view.ID == entry.Contract.Metadata.ID {
				writeJSON(response, view)
				return
			}
		}
	}
	writeError(response, http.StatusNotFound, "contract_not_found", "未找到指定契约")
}

func (server *Server) deactivateContract(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Reason string `json:"reason"`
	}
	if err := decodeJSON(request.Body, &input); err != nil || strings.TrimSpace(input.Reason) == "" {
		writeError(response, http.StatusBadRequest, "reason_required", "停用契约需要填写原因")
		return
	}
	id := request.PathValue("id")
	if strings.HasPrefix(id, "acr-builtin-") {
		writeError(response, http.StatusConflict, "builtin_contract", "内置契约不能停用")
		return
	}
	if err := registry.Retire(request.Context(), server.events, server.config.ProjectID, server.config.WorkspaceID, server.paths.Contracts, id, input.Reason, registry.Source{Kind: registry.SourceConsole}, time.Now()); err != nil {
		writeError(response, http.StatusConflict, "contract_deactivation_failed", err.Error())
		return
	}
	writeJSON(response, map[string]any{"contractId": id, "state": "retired"})
}

func (server *Server) correctMemory(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Claim    string `json:"claim"`
		Reason   string `json:"reason"`
		Revision uint64 `json:"revision"`
	}
	if err := decodeJSON(request.Body, &input); err != nil || strings.TrimSpace(input.Claim) == "" || strings.TrimSpace(input.Reason) == "" {
		writeError(response, http.StatusBadRequest, "invalid_request", "更正需要新的 claim 和原因")
		return
	}
	id := request.PathValue("id")
	current, err := server.memories.Get(request.Context(), server.config.ProjectID, id)
	if err != nil {
		writeError(response, http.StatusNotFound, "memory_not_found", err.Error())
		return
	}
	if input.Revision == 0 {
		input.Revision = current.Revision
	}
	updated, err := server.memories.CorrectAtRevision(request.Context(), server.config.ProjectID, id, input.Revision, memory.CorrectRequest{
		Claim: input.Claim, EvidenceRefs: current.EvidenceRefs, Source: "console", Confidence: 1, Reason: input.Reason,
	})
	if err != nil {
		writeError(response, http.StatusConflict, "memory_correction_failed", err.Error())
		return
	}
	writeJSON(response, makeMemoryView(updated))
}

type compactionGroup struct {
	Keep     string   `json:"keep"`
	Merge    []string `json:"merge"`
	Claim    string   `json:"claim"`
	Evidence []string `json:"evidence"`
}

type compactionView struct {
	Before            int               `json:"before"`
	After             int               `json:"after"`
	EvidencePreserved int               `json:"evidencePreserved"`
	Groups            []compactionGroup `json:"groups"`
}

// compactionPlan groups memories in effect that say the same thing: same kind,
// scope and claim after whitespace and case are normalized. Each group keeps
// its oldest memory, which takes over every piece of evidence of the others,
// so compaction drops no evidence. This is the deterministic first step of
// the compaction pipeline in docs/08; merging claims that only mean the same
// thing needs a model and is not done here.
func (server *Server) compactionPlan(ctx context.Context) (compactionView, []memory.Memory, error) {
	values, err := server.memories.Retrieve(ctx, memory.Query{ProjectID: server.config.ProjectID, Limit: memory.MaximumRetrieveLimit})
	if err != nil {
		return compactionView{}, nil, err
	}
	groups := map[string][]memory.Memory{}
	order := []string{}
	for _, value := range values {
		key := value.Kind + "\x00" + string(value.Scope) + "\x00" + strings.ToLower(strings.Join(strings.Fields(value.Claim), " "))
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], value)
	}
	plan := compactionView{Before: len(values), After: len(values), EvidencePreserved: 100, Groups: []compactionGroup{}}
	for _, key := range order {
		members := groups[key]
		if len(members) < 2 {
			continue
		}
		sort.Slice(members, func(left, right int) bool { return members[left].CreatedAt.Before(members[right].CreatedAt) })
		evidence := map[string]bool{}
		group := compactionGroup{Keep: members[0].MemoryID, Claim: members[0].Claim, Merge: []string{}, Evidence: []string{}}
		for index, member := range members {
			for _, ref := range member.EvidenceRefs {
				if !evidence[ref] {
					evidence[ref] = true
					group.Evidence = append(group.Evidence, ref)
				}
			}
			if index > 0 {
				group.Merge = append(group.Merge, member.MemoryID)
			}
		}
		plan.After -= len(group.Merge)
		plan.Groups = append(plan.Groups, group)
	}
	return plan, values, nil
}

func (server *Server) compactMemories(response http.ResponseWriter, request *http.Request) {
	plan, _, err := server.compactionPlan(request.Context())
	if err != nil {
		writeInternal(response, err)
		return
	}
	writeJSON(response, plan)
}

// applyCompaction carries out the current plan: the kept memory is confirmed
// with the evidence of the whole group, and the others are archived with a
// reason naming it. Both changes are ordinary revisions, so the history of
// every merged memory stays readable.
func (server *Server) applyCompaction(response http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	plan, values, err := server.compactionPlan(ctx)
	if err != nil {
		writeInternal(response, err)
		return
	}
	byID := map[string]memory.Memory{}
	for _, value := range values {
		byID[value.MemoryID] = value
	}
	for _, group := range plan.Groups {
		kept := byID[group.Keep]
		if _, err := server.memories.ConfirmAtRevision(ctx, server.config.ProjectID, kept.MemoryID, kept.Revision, group.Evidence); err != nil {
			writeError(response, http.StatusConflict, "compaction_failed", err.Error())
			return
		}
		for _, id := range group.Merge {
			merged := byID[id]
			if _, err := server.memories.ArchiveAtRevision(ctx, server.config.ProjectID, id, merged.Revision, "merged into "+group.Keep+" by compaction"); err != nil {
				writeError(response, http.StatusConflict, "compaction_failed", err.Error())
				return
			}
		}
	}
	writeJSON(response, plan)
}

func (server *Server) reconcileScan(response http.ResponseWriter, request *http.Request) {
	if server.workspace.Scan == nil {
		writeError(response, http.StatusNotImplemented, "reconcile_unavailable", "当前 Console 未接入工作区对账")
		return
	}
	if err := server.workspace.Scan(request.Context()); err != nil {
		writeError(response, http.StatusConflict, "reconcile_failed", err.Error())
		return
	}
	gaps, err := server.unresolvedGaps(request.Context())
	if err != nil {
		writeInternal(response, err)
		return
	}
	writeJSON(response, map[string]int{"unresolvedGaps": len(gaps)})
}

func (server *Server) reconcileResolve(response http.ResponseWriter, request *http.Request) {
	if server.workspace.Resolve == nil {
		writeError(response, http.StatusNotImplemented, "reconcile_unavailable", "当前 Console 未接入工作区对账")
		return
	}
	var input struct {
		Resolution string `json:"resolution"`
		Reason     string `json:"reason"`
	}
	if err := decodeJSON(request.Body, &input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	event, err := server.workspace.Resolve(request.Context(), request.PathValue("id"), input.Resolution, input.Reason)
	if err != nil {
		writeError(response, http.StatusConflict, "reconcile_failed", err.Error())
		return
	}
	writeJSON(response, makeEventView(event))
}
