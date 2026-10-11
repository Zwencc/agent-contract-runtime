package console

type projectView struct {
	Name        string `json:"name"`
	Workspace   string `json:"workspace"`
	ProjectID   string `json:"projectId"`
	WorkspaceID string `json:"workspaceId"`
}

type metricView struct {
	Label  string `json:"label"`
	Value  any    `json:"value"`
	Delta  string `json:"delta,omitempty"`
	Tone   string `json:"tone"`
	Detail string `json:"detail"`
}

type attentionView struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Route    string `json:"route"`
}

type activityView struct {
	Time     string `json:"time"`
	Passed   int    `json:"passed"`
	Repaired int    `json:"repaired"`
	Denied   int    `json:"denied"`
}

type overviewView struct {
	Guarantee string            `json:"guarantee"`
	Mode      string            `json:"mode"`
	Agent     string            `json:"agent"`
	Metrics   []metricView      `json:"metrics"`
	Attention []attentionView   `json:"attention"`
	Activity  []activityView    `json:"activity"`
	AgentShare []agentShareView `json:"agentShare"`
}

type agentShareView struct {
	Name  string `json:"name"`
	Value int    `json:"value"`
}

type sessionView struct {
	ID            string   `json:"id"`
	NativeID      string   `json:"nativeId"`
	Agent         string   `json:"agent"`
	Guarantee     string   `json:"guarantee"`
	Mode          string   `json:"mode"`
	Status        string   `json:"status"`
	Intent        string   `json:"intent"`
	StartedAt     string   `json:"startedAt"`
	Duration      string   `json:"duration"`
	Actions       int      `json:"actions"`
	Denied        int      `json:"denied"`
	Repaired      int      `json:"repaired"`
	Files         []string `json:"files"`
	Complete      bool     `json:"complete"`
	PreviousAgent string   `json:"previousAgent,omitempty"`
}

type eventView struct {
	Sequence     uint64   `json:"sequence"`
	ID           string   `json:"id"`
	Type         string   `json:"type"`
	Time         string   `json:"time"`
	Agent        string   `json:"agent"`
	Status       string   `json:"status"`
	Summary      string   `json:"summary"`
	Detail       string   `json:"detail"`
	Contract     string   `json:"contract,omitempty"`
	Command      string   `json:"command,omitempty"`
	Resources    []string `json:"resources,omitempty"`
	PreviousHash string   `json:"previousHash"`
	Hash         string   `json:"hash"`
}

type contractView struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	Version       int    `json:"version"`
	Priority      string `json:"priority"`
	State         string `json:"state"`
	Scope         string `json:"scope"`
	Hits          int    `json:"hits"`
	Repairs       int    `json:"repairs"`
	LastHit       string `json:"lastHit"`
	FailureMode   string `json:"failureMode"`
	Applicability string `json:"applicability"`
	Instruction   string `json:"instruction"`
	Verifier      string `json:"verifier"`
}

type memoryView struct {
	ID            string   `json:"id"`
	Claim         string   `json:"claim"`
	Kind          string   `json:"kind"`
	State         string   `json:"state"`
	Priority      int      `json:"priority"`
	Confidence    float64  `json:"confidence"`
	Scope         string   `json:"scope"`
	EvidenceCount int      `json:"evidenceCount"`
	Revisions     uint64   `json:"revisions"`
	UpdatedAt     string   `json:"updatedAt"`
	UsedBy        []string `json:"usedBy"`
}

type graphView struct {
	Nodes []graphNodeView `json:"nodes"`
	Edges []graphEdgeView `json:"edges"`
}

type graphNodeView struct {
	ID     string  `json:"id"`
	Label  string  `json:"label"`
	Kind   string  `json:"kind"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Detail string  `json:"detail"`
}

type graphEdgeView struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Target string `json:"target"`
	Label  string `json:"label"`
}

type agentInsightView struct {
	Agent            string  `json:"agent"`
	Sessions         int     `json:"sessions"`
	FirstPass        float64 `json:"firstPass"`
	RepairSuccess    float64 `json:"repairSuccess"`
	RepeatedAnalysis float64 `json:"repeatedAnalysis"`
	Verification     float64 `json:"verification"`
	Unexplained      int     `json:"unexplained"`
	MedianActions    float64 `json:"medianActions"`
}

type failureView struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Fingerprint string   `json:"fingerprint"`
	Occurrences int      `json:"occurrences"`
	Agents      []string `json:"agents"`
	FirstSeen   string   `json:"firstSeen"`
	LastSeen    string   `json:"lastSeen"`
	Evidence    []uint64 `json:"evidence"`
	Candidate   string   `json:"candidate"`
	Status      string   `json:"status"`
}

type relianceView struct {
	Known                 int                `json:"known"`
	Unknown               int                `json:"unknown"`
	OverReliance          int                `json:"overReliance"`
	UnderReliance         int                `json:"underReliance"`
	AppropriateAcceptance int                `json:"appropriateAcceptance"`
	AppropriateRejection  int                `json:"appropriateRejection"`
	UnverifiedAcceptance  int                `json:"unverifiedAcceptance"`
	ByRisk                []relianceRiskView `json:"byRisk"`
	Observations          []any              `json:"observations"`
}

type relianceRiskView struct {
	Risk          string `json:"risk"`
	Appropriate   int    `json:"appropriate"`
	Inappropriate int    `json:"inappropriate"`
	Unknown       int    `json:"unknown"`
}

type integrityView struct {
	HashChain   string         `json:"hashChain"`
	Coverage    string         `json:"coverage"`
	Hooks       string         `json:"hooks"`
	MCP         string         `json:"mcp"`
	LastSequence uint64        `json:"lastSequence"`
	LastVerified string        `json:"lastVerified"`
	Gaps        []gapView      `json:"gaps"`
	Checks      []integrityCheck `json:"checks"`
}

type gapView struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	DetectedAt string `json:"detectedAt"`
	Resolution string `json:"resolution"`
}

type integrityCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}
