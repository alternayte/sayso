package runner

// The runner protocol (Appendix B). The worker stores a Spec without
// credentials when it creates a run. GET /spec adds the clone token, the
// model and MCP proxy entries and the Skael agent key.

// Stages of an agent run (§8.7).
const (
	StageReview    = "review"
	StageChallenge = "challenge"
	StageReply     = "reply"
	StageFix       = "fix"
)

// Spec is everything the agent runtime needs for one run.
type Spec struct {
	RunID       string            `json:"run_id"`
	Kind        string            `json:"kind" enum:"round,reply,fix,noop"`
	Stages      []string          `json:"stages"`
	Repository  SpecRepository    `json:"repository"`
	BaseSHA     string            `json:"base_sha"`
	HeadSHA     string            `json:"head_sha"`
	PullRequest SpecPullRequest   `json:"pull_request"`
	Ledger      []LedgerEntry     `json:"ledger"`
	Overlay     Overlay           `json:"overlay"`
	Skill       Skill             `json:"skill"`
	Skael       *Skael            `json:"skael,omitempty"`
	Profiles    map[string]string `json:"profiles" doc:"Profile of each stage."`
	Models      []SpecModel       `json:"models" doc:"Targets of the profiles, in fallback order per profile."`
	MCP         []SpecMCP         `json:"mcp"`
	Extensions  []SpecExtension   `json:"extensions"`
	Limits      Limits            `json:"limits"`
	Prompts     Prompts           `json:"prompts"`
	Execution   bool              `json:"execution" doc:"False: no stage may run a command or write a file (SI-21)."`
	Reply       *ReplyInput       `json:"reply,omitempty"`
	Fix         *FixInput         `json:"fix,omitempty"`
}

type SpecRepository struct {
	FullName      string `json:"full_name"`
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch"`
	// Token is a read-only installation token for this repository only.
	// The API mints it for each spec request and never stores it.
	Token string `json:"token,omitempty"`
}

type SpecPullRequest struct {
	Number       int      `json:"number"`
	Title        string   `json:"title"`
	Body         string   `json:"body"`
	Labels       []string `json:"labels"`
	Author       string   `json:"author"`
	DiffStat     string   `json:"diff_stat"`
	ChangedFiles []string `json:"changed_files"`
}

// LedgerEntry is one finding of the review that the agent must check.
type LedgerEntry struct {
	ID             string `json:"id" doc:"S-<seq>"`
	Severity       string `json:"severity"`
	Category       string `json:"category"`
	Title          string `json:"title"`
	Path           string `json:"path"`
	StartLine      int    `json:"start_line"`
	EndLine        int    `json:"end_line"`
	Status         string `json:"status"`
	LastHumanReply string `json:"last_human_reply,omitempty"`
}

// Overlay is the effective .sayso.yml of the base commit (Appendix E).
type Overlay struct {
	Instructions      string   `json:"instructions,omitempty"`
	Ignore            []string `json:"ignore,omitempty"`
	Nits              string   `json:"nits,omitempty"`
	Block             bool     `json:"block"`
	Toolchain         string   `json:"toolchain" enum:"auto,none"`
	Restore           string   `json:"restore" enum:"auto,none"`
	Setup             []string `json:"setup,omitempty"`
	MaxInlineComments int      `json:"max_inline_comments,omitempty"`
}

// Skill is the resolved review skill (§8.4).
type Skill struct {
	Source  string `json:"source" enum:"repository,skael,builtin"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Commit  string `json:"commit,omitempty"`
	// Path of the skill file in /work/base for source repository.
	Path string `json:"path,omitempty"`
	// Files of the skill folder for source builtin, by relative path.
	Files map[string]string `json:"files,omitempty"`
}

// Skael is the Skael set of a run (§14.1).
type Skael struct {
	URL       string `json:"url"`
	AgentKey  string `json:"agent_key,omitempty"`
	Set       string `json:"set"`
	SkillName string `json:"skill_name"`
	Hook      bool   `json:"hook"`
}

// SpecModel is one model target as pi needs it. BaseURL is the model proxy.
type SpecModel struct {
	Profile       string         `json:"profile"`
	Provider      string         `json:"provider"`
	Model         string         `json:"model"`
	API           string         `json:"api,omitempty"`
	BaseURL       string         `json:"base_url,omitempty"`
	Compat        map[string]any `json:"compat,omitempty"`
	ContextWindow int            `json:"context_window,omitempty"`
	MaxTokens     int            `json:"max_tokens,omitempty"`
	Reasoning     string         `json:"reasoning,omitempty"`
	Temperature   *float64       `json:"temperature,omitempty"`
}

// SpecMCP is one MCP server through the MCP proxy.
type SpecMCP struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

// SpecExtension is one enabled pi extension.
type SpecExtension struct {
	Name    string            `json:"name"`
	Package string            `json:"package"`
	Env     map[string]string `json:"env,omitempty"`
}

// Limits of a run (§8.8).
type Limits struct {
	MaxFindings          int    `json:"max_findings"`
	MaxToolCallsPerStage int    `json:"max_tool_calls_per_stage"`
	TimeoutSeconds       int    `json:"timeout_seconds"`
	SetupTimeoutSeconds  int    `json:"setup_timeout_seconds"`
	ChallengeMinSeverity string `json:"challenge_min_severity" enum:"blocker,major,minor,nit"`
}

// Prompts are the guidance layers of §8.9. The contract layer is part of
// the runtime.
type Prompts struct {
	Global     PromptLayer `json:"global"`
	Repository PromptLayer `json:"repository"`
	File       PromptLayer `json:"file"`
}

// PromptLayer is one guidance text with its version or commit.
type PromptLayer struct {
	Version string `json:"version,omitempty"`
	Text    string `json:"text,omitempty"`
}

// ReplyInput is the input of a reply run.
type ReplyInput struct {
	Finding LedgerEntry `json:"finding"`
	Thread  []Comment   `json:"thread"`
	Diff    string      `json:"diff"`
	Action  string      `json:"action,omitempty" enum:",clarify"`
}

// FixInput is the input of a fix run.
type FixInput struct {
	Finding LedgerEntry `json:"finding"`
	Thread  []Comment   `json:"thread"`
}

// Comment is one comment of a thread.
type Comment struct {
	Author string `json:"author"`
	Body   string `json:"body"`
}

// Event is one runtime event in a batch (POST /events).
type Event struct {
	Seq        int64  `json:"seq" minimum:"0" maximum:"999999999"`
	At         string `json:"at" format:"date-time"`
	Stage      string `json:"stage,omitempty"`
	Type       string `json:"type" enum:"model_turn,tool_call,mcp_call,skill_load,finding,env,pkg_summary,note,error"`
	Name       string `json:"name,omitempty"`
	Status     string `json:"status,omitempty"`
	DurationMS int    `json:"duration_ms,omitempty" minimum:"0"`
	TokensIn   int64  `json:"tokens_in,omitempty" minimum:"0"`
	TokensOut  int64  `json:"tokens_out,omitempty" minimum:"0"`
	Preview    string `json:"preview,omitempty"`
	Payload    string `json:"payload,omitempty" doc:"Base64 of gzip JSON."`
}

// Result is result.json (Appendix C).
type Result struct {
	RunID         string          `json:"run_id"`
	Kind          string          `json:"kind" enum:"round,reply,fix,noop"`
	Harness       Harness         `json:"harness"`
	SkillRef      *SkillRef       `json:"skill_ref,omitempty"`
	SkaelManifest *SkaelManifest  `json:"skael_manifest,omitempty"`
	Assessment    *Assessment     `json:"assessment,omitempty"`
	Findings      []Finding       `json:"findings,omitempty"`
	LedgerUpdates []LedgerUpdate  `json:"ledger_updates,omitempty"`
	Reply         *ReplyResult    `json:"reply,omitempty"`
	Patch         *Patch          `json:"patch,omitempty"`
	SkillsLoaded  []SkillLoaded   `json:"skills_loaded,omitempty"`
	Usage         Usage           `json:"usage"`
	PromptRef     *PromptRef      `json:"prompt_ref,omitempty"`
	Environment   []EnvironmentOK `json:"environment,omitempty"`
}

type Harness struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type SkillRef struct {
	Source  string `json:"source" enum:"repository,skael,builtin"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	SHA256  string `json:"sha256"`
}

type SkaelManifest struct {
	Set          string       `json:"set"`
	ResolutionID string       `json:"resolution_id"`
	Skills       []SkaelSkill `json:"skills"`
}

type SkaelSkill struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
	SHA256  string `json:"sha256"`
}

type Assessment struct {
	TLDR        string        `json:"tldr" maxLength:"1200"`
	Risk        string        `json:"risk" enum:"low,medium,high,critical"`
	RiskReasons []string      `json:"risk_reasons"`
	WhereToLook []WhereToLook `json:"where_to_look" maxItems:"6"`
	Impact      []Impact      `json:"impact" maxItems:"6"`
}

type WhereToLook struct {
	Target string `json:"target"`
	Why    string `json:"why"`
}

type Impact struct {
	Area string `json:"area"`
	Why  string `json:"why"`
}

type Finding struct {
	CandidateID string     `json:"candidate_id"`
	Severity    string     `json:"severity" enum:"blocker,major,minor,nit"`
	Category    string     `json:"category"`
	Title       string     `json:"title" maxLength:"100"`
	Body        string     `json:"body" maxLength:"2000"`
	Path        string     `json:"path"`
	StartLine   int        `json:"start_line" minimum:"1"`
	EndLine     int        `json:"end_line" minimum:"1"`
	Evidence    Evidence   `json:"evidence"`
	Suggestion  string     `json:"suggestion,omitempty"`
	SameAs      string     `json:"same_as,omitempty"`
	Challenge   *Challenge `json:"challenge,omitempty"`
}

type Evidence struct {
	Kind     string `json:"kind" enum:"test,trace,call_path,reasoning"`
	Detail   string `json:"detail"`
	TestPath string `json:"test_path,omitempty"`
}

type Challenge struct {
	Outcome string `json:"outcome" enum:"confirmed,refuted,downgraded"`
	Reason  string `json:"reason"`
}

type LedgerUpdate struct {
	FindingID string `json:"finding_id"`
	Status    string `json:"status" enum:"still_open,fixed,withdrawn"`
	Reason    string `json:"reason"`
	Path      string `json:"path,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
}

type ReplyResult struct {
	FindingID string `json:"finding_id"`
	Text      string `json:"text" maxLength:"1500"`
	Action    string `json:"action" enum:"concede,hold,clarify"`
}

type Patch struct {
	FindingID   string `json:"finding_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Diff        string `json:"diff"`
}

type SkillLoaded struct {
	Name    string `json:"name"`
	Version int    `json:"version,omitempty"`
	Stage   string `json:"stage"`
}

type Usage struct {
	TokensIn  int64 `json:"tokens_in"`
	TokensOut int64 `json:"tokens_out"`
}

// PromptRef records the prompt layers of each stage (§8.9).
type PromptRef struct {
	Contract   string            `json:"contract"`
	Global     string            `json:"global,omitempty"`
	Repository string            `json:"repository,omitempty"`
	File       string            `json:"file,omitempty"`
	Skill      string            `json:"skill"`
	Execution  bool              `json:"execution"`
	Stages     map[string]string `json:"stages" doc:"SHA-256 of the assembled prompt of each stage."`
}

// EnvironmentOK is one toolchain, restore or setup step.
type EnvironmentOK struct {
	Step   string `json:"step"`
	Status string `json:"status" enum:"ok,failed,skipped"`
	Detail string `json:"detail,omitempty"`
}

// Complete is the body of POST /complete.
type Complete struct {
	ExitStatus string  `json:"exit_status" enum:"completed,failed,incomplete_output,limit_reached,cancelled"`
	Error      string  `json:"error,omitempty"`
	Result     *Result `json:"result,omitempty"`
}
