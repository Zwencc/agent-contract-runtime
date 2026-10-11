package console

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/projector"
	"github.com/agent-contract-runtime/acr/internal/protocol"
	"github.com/agent-contract-runtime/acr/internal/registry"
)

// Every insight here is computed from recorded events by fixed rules; none
// uses a model, and none is stored except the user's own corrections.

// sessionKey groups an event with its session: the acr launch when there is
// one, so both sides of a launch count together, else the agent session.
func sessionKey(event eventstore.Event) string {
	if event.Actor.ACRSessionID != "" {
		return event.Actor.ACRSessionID
	}
	return event.Actor.NativeSessionID
}

// verificationCommand recognizes commands that check the work.
var verificationCommand = regexp.MustCompile(`\b(go (test|vet)|(npm|pnpm|yarn)( run)? (test|lint|check)|pytest|cargo (test|check|clippy)|make (test|check|verify|lint)|mvn (test|verify)|gradle(w)? test|tsc|eslint|ruff|mypy)\b`)

type agentStats struct {
	sessions         map[string]*sessionStats
	rewrites         int
	rewritesComplete int
	reads            int
	repeatedReads    int
	unexplained      int
}

type sessionStats struct {
	actions  int
	troubled bool
	writes   bool
	verified bool
}

func (server *Server) agentInsights(response http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	events, err := server.allEvents(ctx)
	if err != nil {
		writeInternal(response, err)
		return
	}
	stats := map[string]*agentStats{}
	for _, agent := range []string{"claude-code", "codex"} {
		stats[agent] = &agentStats{sessions: map[string]*sessionStats{}}
	}
	rewritten := map[string]string{} // tool use ID → agent
	lastRead := map[string]bool{}    // session + path → read since its last write
	for _, event := range events {
		agent := stats[string(event.Actor.AgentKind)]
		key := sessionKey(event)
		if agent == nil || key == "" {
			continue
		}
		session := agent.sessions[key]
		if session == nil {
			session = &sessionStats{}
			agent.sessions[key] = session
		}
		payload := payloadMap(event.Payload)
		switch event.Type {
		case "action.proposed":
			session.actions++
			var action protocol.Action
			json.Unmarshal(event.Payload, &action)
			if verificationCommand.MatchString(commandFromPayload(payload)) {
				session.verified = true
			}
			for _, path := range action.Resources {
				marker := key + "\x00" + path
				switch action.OperationKind {
				case "filesystem.read":
					agent.reads++
					if lastRead[marker] {
						agent.repeatedReads++
					}
					lastRead[marker] = true
				case "filesystem.write":
					session.writes = true
					delete(lastRead, marker)
				}
			}
		case "action.denied", "action.failed":
			session.troubled = true
		case "action.rewritten":
			agent.rewrites++
			rewritten[event.Causality.NativeToolUseID] = string(event.Actor.AgentKind)
		case "action.completed":
			if owner, ok := rewritten[event.Causality.NativeToolUseID]; ok && event.Causality.NativeToolUseID != "" {
				stats[owner].rewritesComplete++
				delete(rewritten, event.Causality.NativeToolUseID)
			}
		}
	}
	gaps, err := server.eventsOfTypes(ctx, "coverage.gap_detected")
	if err != nil {
		writeInternal(response, err)
		return
	}
	for _, gap := range gaps {
		if agent := stats[string(gap.Actor.AgentKind)]; agent != nil {
			agent.unexplained++
		}
	}
	result := []agentInsightView{}
	for _, name := range []string{"claude-code", "codex"} {
		agent := stats[name]
		view := agentInsightView{Agent: name, Sessions: len(agent.sessions), Unexplained: agent.unexplained}
		actions := []int{}
		clean, writing, verified := 0, 0, 0
		for _, session := range agent.sessions {
			actions = append(actions, session.actions)
			if !session.troubled {
				clean++
			}
			if session.writes {
				writing++
				if session.verified {
					verified++
				}
			}
		}
		sort.Ints(actions)
		if len(actions) > 0 {
			view.MedianActions = float64(actions[len(actions)/2])
			view.FirstPass = percent(clean, len(actions))
		}
		view.RepairSuccess = percent(agent.rewritesComplete, agent.rewrites)
		view.Verification = percent(verified, writing)
		view.RepeatedAnalysis = percent(agent.repeatedReads, agent.reads)
		result = append(result, view)
	}
	writeJSON(response, result)
}

func percent(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return float64(int(1000*float64(part)/float64(whole)+0.5)) / 10
}

func (server *Server) failures(response http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	if _, err := server.projections.Sync(ctx, server.events, server.config.ProjectID, server.paths.Root); err != nil {
		writeInternal(response, err)
		return
	}
	relevant, err := server.projections.Relevant(ctx, server.config.ProjectID, nil, projector.MaximumRelevant)
	if err != nil {
		writeInternal(response, err)
		return
	}
	result := []failureView{}
	for _, failure := range relevant.Failures {
		view := failureView{ID: failure.Node.ID, Title: "重复失败：" + failure.Attributes.Tool, Fingerprint: failure.Attributes.Signature, Occurrences: failure.Attributes.Occurrences,
			Agents: []string{}, Evidence: []uint64{}, Candidate: "none", Status: "open"}
		agents := map[string]bool{}
		for _, sequence := range []uint64{failure.Node.ValidFromSequence, failure.Attributes.LastSequence} {
			if sequence == 0 {
				continue
			}
			events, err := server.events.List(ctx, server.config.ProjectID, sequence-1, 1)
			if err != nil || len(events) == 0 {
				continue
			}
			event := events[0]
			when := event.OccurredAt.Local().Format("2006-01-02 15:04")
			if sequence == failure.Node.ValidFromSequence {
				view.FirstSeen = when
			}
			if sequence == failure.Attributes.LastSequence {
				view.LastSeen = when
			}
			if agent := string(event.Actor.AgentKind); agent != "" && !agents[agent] {
				agents[agent] = true
				view.Agents = append(view.Agents, agent)
			}
			if len(view.Evidence) == 0 || view.Evidence[len(view.Evidence)-1] != sequence {
				view.Evidence = append(view.Evidence, sequence)
			}
		}
		// The same failure in several agents is worth a shared rule; one
		// repeated failure is worth remembering.
		switch {
		case len(view.Agents) > 1 || view.Occurrences >= 3:
			view.Candidate = "contract"
		case view.Occurrences >= 2:
			view.Candidate = "memory"
		}
		result = append(result, view)
	}
	writeJSON(response, result)
}

type relianceObservation struct {
	ID             string `json:"id"`
	Recommendation string `json:"recommendation"`
	Decision       string `json:"decision"`
	Outcome        string `json:"outcome"`
	Risk           string `json:"risk"`
	Evidence       string `json:"evidence"`
}

// reliance measures whether the user accepted the actions ACR put to them at
// the right times. Each time a contract asked for confirmation is a
// recommendation; running the action afterwards is acceptance, otherwise it
// was rejected or left unanswered; the outcome is incorrect when the action
// failed or the next verification after it failed, correct when that
// verification passed, and unknown otherwise. Without an outcome the
// decision is shown but never judged.
func (server *Server) reliance(response http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	events, err := server.eventsOfTypes(ctx, "action.asked", "action.completed", "action.failed", "verification.passed", "verification.failed")
	if err != nil {
		writeInternal(response, err)
		return
	}
	severity := map[string]string{}
	if values, err := registry.Load(ctx, server.events, server.config.ProjectID, server.paths.Contracts); err == nil {
		for _, value := range values {
			severity[value.Metadata.ID] = value.Metadata.Severity
		}
	}
	type asked struct {
		event   eventstore.Event
		outcome eventstore.Event
		next    eventstore.Event
	}
	pending := map[string]*asked{}
	order := []*asked{}
	waiting := []*asked{}
	for _, event := range events {
		switch event.Type {
		case "action.asked":
			item := &asked{event: event}
			order = append(order, item)
			if id := event.Causality.NativeToolUseID; id != "" {
				pending[id] = item
			}
		case "action.completed", "action.failed":
			if item := pending[event.Causality.NativeToolUseID]; item != nil && event.Causality.NativeToolUseID != "" {
				item.outcome = event
				delete(pending, event.Causality.NativeToolUseID)
				if event.Type == "action.completed" {
					waiting = append(waiting, item)
				}
			}
		case "verification.passed", "verification.failed":
			for _, item := range waiting {
				if item.next.EventID == "" && event.OccurredAt.Sub(item.outcome.OccurredAt) <= 30*time.Minute {
					item.next = event
				}
			}
			waiting = waiting[:0]
		}
	}
	view := relianceView{ByRisk: []relianceRiskView{}, Observations: []any{}}
	byRisk := map[string]*relianceRiskView{}
	for _, risk := range []string{"R0", "R1", "R2", "R3"} {
		byRisk[risk] = &relianceRiskView{Risk: risk}
	}
	for _, item := range order {
		payload := payloadMap(item.event.Payload)
		risk := riskOf(contractFromPayload(payload), severity)
		decision, outcome := "rejected_or_unanswered", "unknown"
		evidence := fmt.Sprintf("#%d", item.event.Sequence)
		if item.outcome.EventID != "" {
			decision = "accepted"
			evidence += fmt.Sprintf(" → #%d", item.outcome.Sequence)
			switch {
			case item.outcome.Type == "action.failed" || item.next.Type == "verification.failed":
				outcome = "incorrect"
			case item.next.Type == "verification.passed":
				outcome = "correct"
			}
			if item.next.EventID != "" {
				evidence += fmt.Sprintf(" → #%d", item.next.Sequence)
			}
		}
		bucket := byRisk[risk]
		switch {
		case outcome == "unknown":
			view.Unknown++
			bucket.Unknown++
			if decision == "accepted" {
				view.UnverifiedAcceptance++
			}
		case decision == "accepted" && outcome == "correct":
			view.Known++
			view.AppropriateAcceptance++
			bucket.Appropriate++
		case decision == "accepted" && outcome == "incorrect":
			view.Known++
			view.OverReliance++
			bucket.Inappropriate++
		}
		view.Observations = append(view.Observations, relianceObservation{ID: item.event.EventID, Recommendation: firstString(payload, "reason"),
			Decision: decision, Outcome: outcome, Risk: risk, Evidence: evidence})
	}
	for _, risk := range []string{"R0", "R1", "R2", "R3"} {
		view.ByRisk = append(view.ByRisk, *byRisk[risk])
	}
	writeJSON(response, view)
}

// riskOf maps the asking contract's severity to the risk levels of docs/13:
// critical contracts guard irreversible actions such as production data.
func riskOf(contractID string, severity map[string]string) string {
	switch severity[contractID] {
	case "critical":
		return "R3"
	case "high":
		return "R2"
	}
	return "R1"
}

type interactionSignal struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	Confidence float64 `json:"confidence"`
	Time       string  `json:"time"`
	Evidence   string  `json:"evidence"`
	Suggestion string  `json:"suggestion"`
	Corrected  bool    `json:"corrected,omitempty"`
}

// interactionPatterns are the explicit words of docs/13 §7.2. They describe
// the conversation, never the person.
var interactionPatterns = []struct {
	kind       string
	pattern    *regexp.Regexp
	suggestion string
}{
	{"confusion", regexp.MustCompile(`(?i)不明白|看不懂|没看懂|什么意思|怎么回事|没懂|搞不清|don'?t understand|confus`), "把最近的事件折叠成三步解释：做了什么、为什么、结果如何"},
	{"frustration", regexp.MustCompile(`(?i)又失败|还是不行|怎么又|又错|还是没|一直失败|又出错|still (not|fail)|again\?`), "暂停新的尝试，回放最后一次成功路径再继续"},
	{"urgency", regexp.MustCompile(`(?i)赶紧|马上|立刻|尽快|急用|很急|asap|urgent|right now`), "保持契约边界不变，突出最短的安全路径"},
	{"distrust", regexp.MustCompile(`(?i)你确定|真的吗|别骗|不要瞎|靠谱吗|are you sure`), "展示这一步依据的事件和验证证据"},
	{"hesitation", regexp.MustCompile(`(?i)要不要|是不是应该|不太确定|再想想|算了吧|should we|not sure`), "列出两三个选项和各自的证据，不催促决定"},
}

type interactionSettings struct {
	Enabled     bool            `json:"enabled"`
	Corrections map[string]bool `json:"corrections"`
}

var interactionSettingsMutex sync.Mutex

func (server *Server) interactionSettingsPath() string {
	return filepath.Join(server.paths.State, "console-interaction.json")
}

func (server *Server) loadInteractionSettings() interactionSettings {
	settings := interactionSettings{Corrections: map[string]bool{}}
	if data, err := os.ReadFile(server.interactionSettingsPath()); err == nil {
		json.Unmarshal(data, &settings)
	}
	if settings.Corrections == nil {
		settings.Corrections = map[string]bool{}
	}
	return settings
}

func (server *Server) saveInteractionSettings(settings interactionSettings) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(server.interactionSettingsPath(), append(data, '\n'), 0o600)
}

// interactionSignals derives signals from the user's own prompts and the
// failures of the last day. They change how the console helps, never what
// an agent may do.
func (server *Server) interactionSignals(ctx context.Context, settings interactionSettings) ([]interactionSignal, error) {
	since := time.Now().Add(-24 * time.Hour)
	events, err := server.events.Latest(ctx, server.config.ProjectID, maximumEvents, eventstore.Filter{Types: []string{"intent.observed", "action.failed"}})
	if err != nil {
		return nil, err
	}
	signals := []interactionSignal{}
	add := func(kind string, confidence float64, event eventstore.Event, evidence, suggestion string) {
		sum := sha256.Sum256([]byte(kind + "\x00" + event.EventID))
		id := "sig_" + hex.EncodeToString(sum[:8])
		signals = append(signals, interactionSignal{ID: id, Kind: kind, Confidence: confidence, Time: event.OccurredAt.Local().Format("2006-01-02 15:04"),
			Evidence: evidence, Suggestion: suggestion, Corrected: settings.Corrections[id]})
	}
	prompts := map[string][]eventstore.Event{}
	failures := map[string][]eventstore.Event{}
	for _, event := range events {
		if event.OccurredAt.Before(since) {
			continue
		}
		key := sessionKey(event)
		if event.Type == "action.failed" {
			failures[key] = append(failures[key], event)
			continue
		}
		if event.Actor.Kind != "user" {
			continue
		}
		prompt := firstString(payloadMap(event.Payload), "prompt")
		for _, rule := range interactionPatterns {
			if match := rule.pattern.FindString(prompt); match != "" {
				add(rule.kind, 0.6, event, fmt.Sprintf("用户消息中出现“%s”", match), rule.suggestion)
			}
		}
		normalized := strings.Join(strings.Fields(prompt), " ")
		for _, earlier := range prompts[key] {
			if strings.Join(strings.Fields(firstString(payloadMap(earlier.Payload), "prompt")), " ") == normalized && event.OccurredAt.Sub(earlier.OccurredAt) <= 30*time.Minute {
				add("frustration", 0.7, event, "30 分钟内重复了同一个请求", "暂停新的尝试，回放最后一次成功路径再继续")
				break
			}
		}
		prompts[key] = append(prompts[key], event)
	}
	for _, list := range failures {
		for index := range list {
			window := 1
			for next := index + 1; next < len(list) && list[next].OccurredAt.Sub(list[index].OccurredAt) <= 10*time.Minute; next++ {
				window++
			}
			if window >= 5 {
				add("high_cognitive_load", 0.7, list[index], fmt.Sprintf("10 分钟内有 %d 次工具调用失败", window), "减少同时出现的候选项，一次只处理一个失败")
				break
			}
		}
	}
	sort.SliceStable(signals, func(left, right int) bool { return signals[left].Time > signals[right].Time })
	return signals, nil
}

// interactionState is off unless the user turned it on (docs/13 §12.2).
func (server *Server) interactionState(response http.ResponseWriter, request *http.Request) {
	interactionSettingsMutex.Lock()
	settings := server.loadInteractionSettings()
	interactionSettingsMutex.Unlock()
	if !settings.Enabled {
		writeJSON(response, []interactionSignal{})
		return
	}
	signals, err := server.interactionSignals(request.Context(), settings)
	if err != nil {
		writeInternal(response, err)
		return
	}
	writeJSON(response, signals)
}

func (server *Server) interactionFeedback(response http.ResponseWriter, request *http.Request) {
	var input struct {
		ObservationID string `json:"observation_id"`
		Accurate      bool   `json:"accurate"`
	}
	if err := decodeJSON(request.Body, &input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	interactionSettingsMutex.Lock()
	defer interactionSettingsMutex.Unlock()
	settings := server.loadInteractionSettings()
	signals, err := server.interactionSignals(request.Context(), settings)
	if err != nil {
		writeInternal(response, err)
		return
	}
	for _, signal := range signals {
		if signal.ID != input.ObservationID {
			continue
		}
		// A correction only marks the signal; it is never read as a new label.
		if input.Accurate {
			delete(settings.Corrections, signal.ID)
		} else {
			settings.Corrections[signal.ID] = true
		}
		if err := server.saveInteractionSettings(settings); err != nil {
			writeInternal(response, err)
			return
		}
		signal.Corrected = !input.Accurate
		writeJSON(response, signal)
		return
	}
	writeError(response, http.StatusNotFound, "observation_not_found", "没有可纠正的交互状态观测")
}

func (server *Server) interactionSettings(response http.ResponseWriter, _ *http.Request) {
	interactionSettingsMutex.Lock()
	defer interactionSettingsMutex.Unlock()
	settings := server.loadInteractionSettings()
	writeJSON(response, map[string]any{"enabled": settings.Enabled, "corrections": len(settings.Corrections),
		"scope": "只读取最近 24 小时内用户自己的消息和工具失败次数；不保存逐句标签；只改变 Console 的辅助方式，不影响任何契约或 Agent 权限"})
}

func (server *Server) updateInteractionSettings(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(request.Body, &input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	interactionSettingsMutex.Lock()
	defer interactionSettingsMutex.Unlock()
	settings := server.loadInteractionSettings()
	settings.Enabled = input.Enabled
	if err := server.saveInteractionSettings(settings); err != nil {
		writeInternal(response, err)
		return
	}
	writeJSON(response, map[string]bool{"enabled": settings.Enabled})
}

// clearInteractionState deletes everything derived about the user: the
// corrections, and the setting itself.
func (server *Server) clearInteractionState(response http.ResponseWriter, _ *http.Request) {
	interactionSettingsMutex.Lock()
	defer interactionSettingsMutex.Unlock()
	if err := os.Remove(server.interactionSettingsPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		writeInternal(response, err)
		return
	}
	writeJSON(response, map[string]bool{"enabled": false})
}

// memoryUsage lists, per memory, the agents ACR told about it: the
// corrections delivered to their sessions.
func (server *Server) memoryUsage(ctx context.Context) (map[string][]string, error) {
	events, err := server.eventsOfTypes(ctx, "context.delivered")
	if err != nil {
		return nil, err
	}
	usage := map[string][]string{}
	for _, event := range events {
		var payload struct {
			MemoryRevisions []string `json:"memory_revisions"`
		}
		if json.Unmarshal(event.Payload, &payload) != nil || event.Actor.AgentKind == "" {
			continue
		}
		for _, key := range payload.MemoryRevisions {
			id, _, _ := strings.Cut(key, "@")
			agent := string(event.Actor.AgentKind)
			found := false
			for _, existing := range usage[id] {
				found = found || existing == agent
			}
			if !found {
				usage[id] = append(usage[id], agent)
			}
		}
	}
	return usage, nil
}
