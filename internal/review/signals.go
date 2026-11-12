// Package review holds the PRReview and ReviewRound workflows and their
// activities. The webhook handler and the reconciler start or signal the
// PRReview workflow with the names and payloads in this file.
package review

import (
	"fmt"

	"github.com/google/uuid"
)

const (
	// WorkflowType is the type name of the per pull request workflow.
	WorkflowType = "PRReview"
	// TaskQueue is the queue of workflows and short activities (§3.1).
	TaskQueue = "reviews"
	// AgentRunsQueue is the queue of agent run activities (§3.1).
	AgentRunsQueue = "agent-runs"
)

// Signal names (§6.3).
const (
	SignalHeadUpdated  = "head_updated"
	SignalPausedDraft  = "paused_draft"
	SignalClosed       = "closed"
	SignalCommand      = "command"
	SignalThreadReply  = "thread_reply"
	SignalHumanComment = "human_comment"
	SignalThreadState  = "thread_state"
)

// Triggers of a round.
const (
	TriggerOpened         = "opened"
	TriggerReady          = "ready"
	TriggerPush           = "push"
	TriggerCommand        = "command"
	TriggerCheckRerequest = "check_rerequest"
	TriggerReconcile      = "reconcile"
)

// WorkflowID is the ID of the PRReview workflow of one pull request.
func WorkflowID(repoGitHubID int64, number int) string {
	return fmt.Sprintf("pr/%d/%d", repoGitHubID, number)
}

// Start is the input of the PRReview workflow. It holds IDs only (D-01).
type Start struct {
	RepositoryID uuid.UUID `json:"repository_id"`
	RepoGitHubID int64     `json:"repo_github_id"`
	Number       int       `json:"number"`
}

// Signal is the payload of every PRReview signal. It holds IDs, SHAs and
// flags only; activities read text from GitHub or PostgreSQL.
type Signal struct {
	DeliveryID string `json:"delivery_id,omitempty"`
	// head_updated
	Trigger string `json:"trigger,omitempty"`
	HeadSHA string `json:"head_sha,omitempty"`
	BaseSHA string `json:"base_sha,omitempty"`
	Forced  bool   `json:"forced,omitempty"`
	// closed
	Merged bool `json:"merged,omitempty"`
	// command, thread_reply, human_comment: the comment that carries the text.
	CommentID     int64  `json:"comment_id,omitempty"`
	CommentSource string `json:"comment_source,omitempty"` // issue or review
	InReplyTo     int64  `json:"in_reply_to,omitempty"`
	// thread_state
	ThreadNodeID string `json:"thread_node_id,omitempty"`
	Resolved     bool   `json:"resolved,omitempty"`
}
