package console

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/agent-contract-runtime/acr/internal/eventstore"
)

// verifiedHead remembers the last full verification of the event chain, so
// the integrity page verifies again only after the chain has grown.
type verifiedHead struct {
	mutex    sync.Mutex
	sequence uint64
	hash     string
	at       time.Time
	err      error
}

// verifyChain verifies the whole chain unless its head is the one verified
// last time. It returns when the result was computed.
func (server *Server) verifyChain(ctx context.Context) (time.Time, error) {
	head, headErr := server.events.Head(ctx, server.config.ProjectID)
	cache := &server.verified
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	if headErr == nil && !cache.at.IsZero() && cache.sequence == head.Sequence && cache.hash == head.EventHash {
		return cache.at, cache.err
	}
	err := server.events.Verify(ctx, server.config.ProjectID)
	cache.sequence, cache.hash, cache.at, cache.err = head.Sequence, head.EventHash, time.Now(), err
	return cache.at, err
}

// hookHealth says whether the agents' hooks are installed and have reported:
// installed and reporting is healthy, installed but silent is a warning, and
// no hooks at all is critical, because then ACR sees nothing.
func (server *Server) hookHealth(ctx context.Context) (string, string) {
	installed := map[string]bool{
		"claude-code": fileContains(filepath.Join(server.paths.Root, ".claude", "settings.json"), " hook claude ", "'hook' 'claude'"),
		"codex":       fileContains(filepath.Join(server.paths.Root, ".codex", "hooks.json"), " hook codex ", "'hook' 'codex'"),
	}
	if !installed["claude-code"] && !installed["codex"] {
		return "critical", "没有安装任何 Agent Hook，ACR 看不到 Agent 的操作"
	}
	reported := map[string]bool{}
	events, err := server.events.Latest(ctx, server.config.ProjectID, 500, eventstore.Filter{Types: []string{"session.started", "intent.observed", "action.completed", "action.failed", "turn.stopped"}})
	if err != nil {
		return "unknown", err.Error()
	}
	for _, event := range events {
		// Guarded server events use the launch as their session; they are not hooks.
		if event.Actor.NativeSessionID != "" && event.Actor.NativeSessionID != event.Actor.ACRSessionID {
			reported[string(event.Actor.AgentKind)] = true
		}
	}
	silent := []string{}
	for _, agent := range []string{"claude-code", "codex"} {
		if installed[agent] && !reported[agent] {
			silent = append(silent, agent)
		}
	}
	if len(silent) > 0 {
		return "warning", strings.Join(silent, "、") + " 的 Hook 已安装，但最近没有上报事件：可能未受信任、被关闭或失败"
	}
	return "healthy", "已安装的 Hook 都在上报事件"
}

// mcpHealth says whether the memory and rule tools are registered for the
// agents, so they can carry out what the user asks them to remember.
func (server *Server) mcpHealth() (string, string) {
	claude := fileContains(filepath.Join(server.paths.Root, ".mcp.json"), `"acr"`)
	codex := fileContains(filepath.Join(server.paths.Root, ".codex", "config.toml"), "[mcp_servers.acr]")
	switch {
	case claude && codex:
		return "healthy", "Claude Code 与 Codex 都已注册 ACR 工具"
	case claude || codex:
		return "warning", "只有一个 Agent 注册了 ACR 工具；运行 acr install-hooks --agent all --apply"
	}
	return "warning", "未注册 ACR 工具，Agent 无法按用户要求记录记忆和规则"
}

func fileContains(path string, needles ...string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, needle := range needles {
		if strings.Contains(string(data), needle) {
			return true
		}
	}
	return false
}
