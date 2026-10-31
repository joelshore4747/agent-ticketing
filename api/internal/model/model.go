// Package model holds the row structs and the string enums. Enum values are lowercase snake_case
// everywhere: Postgres text, JSON and GraphQL. In Go that is simply a defined string type — the
// zero value is the empty string rather than a variant, so anything arriving from outside goes
// through a Parse function before it is trusted.
package model

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/joelshore/ticketing/api/internal/apperr"
)

type TicketType string

const (
	TypeBug         TicketType = "bug"
	TypeFeature     TicketType = "feature"
	TypeImprovement TicketType = "improvement"
	TypeChore       TicketType = "chore"
	TypeResearch    TicketType = "research"
	TypeDesign      TicketType = "design"
	TypeRefactor    TicketType = "refactor"
	// TypeEval is the gate that closes a planned batch: the batch cannot finish until it is done.
	TypeEval TicketType = "eval"
)

var TicketTypes = []TicketType{
	TypeBug, TypeFeature, TypeImprovement, TypeChore, TypeResearch, TypeDesign, TypeRefactor, TypeEval,
}

type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityMedium Priority = "medium"
	PriorityHigh   Priority = "high"
	PriorityUrgent Priority = "urgent"
)

var Priorities = []Priority{PriorityLow, PriorityMedium, PriorityHigh, PriorityUrgent}

type Status string

const (
	StatusTriage     Status = "triage"
	StatusOpen       Status = "open"
	StatusInProgress Status = "in_progress"
	StatusDone       Status = "done"
	StatusClosed     Status = "closed"
)

var Statuses = []Status{StatusTriage, StatusOpen, StatusInProgress, StatusDone, StatusClosed}

// ActiveStatuses are the ones that count as "still being worked on".
var ActiveStatuses = []Status{StatusTriage, StatusOpen, StatusInProgress}

// IsTerminal reports whether a status needs a resolution to enter.
func (s Status) IsTerminal() bool { return s == StatusDone || s == StatusClosed }

type Source string

const (
	SourceAI    Source = "ai"
	SourceHuman Source = "human"
)

var Sources = []Source{SourceAI, SourceHuman}

type BatchKind string

const (
	BatchAudit  BatchKind = "audit"
	BatchReview BatchKind = "review"
	BatchPrompt BatchKind = "prompt"
	// BatchPlan is work decided in advance rather than findings reported after the fact. It is why
	// this is a batch and no longer an audit run.
	BatchPlan BatchKind = "plan"
)

var BatchKinds = []BatchKind{BatchAudit, BatchReview, BatchPrompt, BatchPlan}

// CheckpointDecision is what a human decides once a batch's eval ticket is done. The next batch in
// the roadmap opens on continue or adjust; stop leaves it locked.
type CheckpointDecision string

const (
	CheckpointContinue CheckpointDecision = "continue"
	CheckpointAdjust   CheckpointDecision = "adjust"
	CheckpointStop     CheckpointDecision = "stop"
)

var CheckpointDecisions = []CheckpointDecision{CheckpointContinue, CheckpointAdjust, CheckpointStop}

type PullRequestState string

const (
	PRStateOpen   PullRequestState = "open"
	PRStateMerged PullRequestState = "merged"
	PRStateClosed PullRequestState = "closed"
)

var PullRequestStates = []PullRequestState{PRStateOpen, PRStateMerged, PRStateClosed}

type LinkKind string

const (
	LinkRelatesTo  LinkKind = "relates_to"
	LinkBlocks     LinkKind = "blocks"
	LinkDuplicates LinkKind = "duplicates"
)

// parseEnum is the one place an outside string becomes an enum, so every transport rejects a bad
// value the same way and with the same message.
func parseEnum[T ~string](raw string, allowed []T, name string) (T, error) {
	var zero T
	for _, candidate := range allowed {
		if string(candidate) == raw {
			return candidate, nil
		}
	}
	names := make([]string, len(allowed))
	for i, candidate := range allowed {
		names[i] = string(candidate)
	}
	return zero, apperr.BadRequestf("invalid %s: %q (expected one of %s)", name, raw, strings.Join(names, ", "))
}

func ParseTicketType(raw string) (TicketType, error) {
	return parseEnum(raw, TicketTypes, "TicketType")
}

func ParsePriority(raw string) (Priority, error) {
	return parseEnum(raw, Priorities, "Priority")
}

func ParseStatus(raw string) (Status, error) {
	return parseEnum(raw, Statuses, "Status")
}

func ParseSource(raw string) (Source, error) {
	return parseEnum(raw, Sources, "Source")
}

func ParseBatchKind(raw string) (BatchKind, error) {
	return parseEnum(raw, BatchKinds, "BatchKind")
}

func ParsePullRequestState(raw string) (PullRequestState, error) {
	return parseEnum(raw, PullRequestStates, "PullRequestState")
}

// --- rows -------------------------------------------------------------------

type Project struct {
	ID               uuid.UUID `json:"id" db:"id"`
	Key              string    `json:"key" db:"key"`
	Name             string    `json:"name" db:"name"`
	RepoPath         *string   `json:"repo_path" db:"repo_path"`
	NextTicketNumber int32     `json:"next_ticket_number" db:"next_ticket_number"`
	CreatedAt        time.Time `json:"created_at" db:"created_at"`
}

type Area struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	ProjectID    uuid.UUID  `json:"project_id" db:"project_id"`
	Name         string     `json:"name" db:"name"`
	Description  string     `json:"description" db:"description"`
	PathGlobs    []string   `json:"path_globs" db:"path_globs"`
	AutoAcceptAI bool       `json:"auto_accept_ai" db:"auto_accept_ai"`
	ArchivedAt   *time.Time `json:"archived_at" db:"archived_at"`
	CreatedAt    time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at" db:"updated_at"`
	// OpenCount is tickets in triage/open/in_progress; computed by the select, never stored.
	OpenCount int64 `json:"open_count" db:"open_count"`
}

type Batch struct {
	ID        uuid.UUID `json:"id" db:"id"`
	ProjectID uuid.UUID `json:"project_id" db:"project_id"`
	Kind      BatchKind `json:"kind" db:"kind"`
	Actor     string    `json:"actor" db:"actor"`
	Title     string    `json:"title" db:"title"`
	// Brief is why this work is being done, in the author's words. Served to whoever picks up a
	// ticket from the batch.
	Brief         string     `json:"brief" db:"brief"`
	GitRef        *string    `json:"git_ref" db:"git_ref"`
	Summary       *string    `json:"summary" db:"summary"`
	FindingsCount int32      `json:"findings_count" db:"findings_count"`
	StartedAt     time.Time  `json:"started_at" db:"started_at"`
	FinishedAt    *time.Time `json:"finished_at" db:"finished_at"`
	RoadmapID     *uuid.UUID `json:"roadmap_id" db:"roadmap_id"`
	Sequence      *int32     `json:"sequence" db:"sequence"`
	// What the batch builds and how; Brief is why. Together they are the human read of a batch.
	What               string              `json:"what" db:"what"`
	How                string              `json:"how" db:"how"`
	CheckpointDecision *CheckpointDecision `json:"checkpoint_decision" db:"checkpoint_decision"`
	CheckpointNote     *string             `json:"checkpoint_note" db:"checkpoint_note"`
	CheckpointBy       *string             `json:"checkpoint_by" db:"checkpoint_by"`
	CheckpointAt       *time.Time          `json:"checkpoint_at" db:"checkpoint_at"`
	// Digest is the plain-English account finish_batch computes; nobody writes it (TP-87).
	Digest   *string    `json:"digest" db:"digest"`
	DigestAt *time.Time `json:"digest_at" db:"digest_at"`
	// ReviewsRequired: a before-review to start and an after-review to finish (TP-108).
	ReviewsRequired bool `json:"reviews_required" db:"reviews_required"`
	// How big the batch's change was (TP-160), reported in by scripts/batch-size.sh. Null until it is.
	// LinesAdded/LinesDeleted are the whole diff; TestLines* say how much of it was test files, so the
	// ceiling can count code only (TP-212). The test pair is null for a batch measured before the split.
	LinesAdded       *int32     `json:"lines_added" db:"lines_added"`
	LinesDeleted     *int32     `json:"lines_deleted" db:"lines_deleted"`
	TestLinesAdded   *int32     `json:"test_lines_added" db:"test_lines_added"`
	TestLinesDeleted *int32     `json:"test_lines_deleted" db:"test_lines_deleted"`
	SizeBaseCommit   *string    `json:"size_base_commit" db:"size_base_commit"`
	SizeReportedAt   *time.Time `json:"size_reported_at" db:"size_reported_at"`
}

// ProjectReview is the project memory at one commit, taken before or after a batch (goal 2).
type ProjectReview struct {
	ID        uuid.UUID `json:"id" db:"id"`
	ProjectID uuid.UUID `json:"project_id" db:"project_id"`
	BatchID   uuid.UUID `json:"batch_id" db:"batch_id"`
	Phase     string    `json:"phase" db:"phase"`
	Commit    string    `json:"commit" db:"commit"`
	Body      string    `json:"body" db:"body"`
	Actor     string    `json:"actor" db:"actor"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

type Roadmap struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	ProjectID  uuid.UUID  `json:"project_id" db:"project_id"`
	Title      string     `json:"title" db:"title"`
	Spec       string     `json:"spec" db:"spec"`
	CreatedAt  time.Time  `json:"created_at" db:"created_at"`
	ArchivedAt *time.Time `json:"archived_at" db:"archived_at"`
}

type Tag struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	ProjectID   uuid.UUID  `json:"project_id" db:"project_id"`
	Name        string     `json:"name" db:"name"`
	Description string     `json:"description" db:"description"`
	ArchivedAt  *time.Time `json:"archived_at" db:"archived_at"`
	CreatedAt   time.Time  `json:"created_at" db:"created_at"`
}

// Ticket is the tickets row, minus the generated search column.
type Ticket struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	ProjectID   uuid.UUID  `json:"project_id" db:"project_id"`
	Number      int32      `json:"number" db:"number"`
	Key         string     `json:"key" db:"key"`
	Title       string     `json:"title" db:"title"`
	Description string     `json:"description" db:"description"`
	Type        TicketType `json:"type" db:"type"`
	Priority    Priority   `json:"priority" db:"priority"`
	Status      Status     `json:"status" db:"status"`
	Source      Source     `json:"source" db:"source"`
	AreaID      *uuid.UUID `json:"area_id" db:"area_id"`
	BatchID     *uuid.UUID `json:"batch_id" db:"batch_id"`
	// BatchPosition orders a plan batch; NULL outside a batch and in findings batches.
	BatchPosition *int32     `json:"batch_position" db:"batch_position"`
	Fingerprint   *string    `json:"fingerprint" db:"fingerprint"`
	SeenCount     int32      `json:"seen_count" db:"seen_count"`
	LastSeenAt    time.Time  `json:"last_seen_at" db:"last_seen_at"`
	Branch        *string    `json:"branch" db:"branch"`
	Resolution    *string    `json:"resolution" db:"resolution"`
	ClaimedBy     *string    `json:"claimed_by" db:"claimed_by"`
	ClaimedAt     *time.Time `json:"claimed_at" db:"claimed_at"`
	Version       int32      `json:"version" db:"version"`
	CreatedAt     time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at" db:"updated_at"`
	CompletedAt   *time.Time `json:"completed_at" db:"completed_at"`

	// Section A: the human's, set over GraphQL only. Success criteria are TicketCriterion rows.
	Intent      string `json:"intent" db:"intent"`
	Constraints string `json:"constraints" db:"constraints"`
	NonGoals    string `json:"non_goals" db:"non_goals"`
	// Section B: the AI's proposal, approved by a human.
	Approach           string     `json:"approach" db:"approach"`
	RiskAndRollback    string     `json:"risk_and_rollback" db:"risk_and_rollback"`
	WorkedExample      string     `json:"worked_example" db:"worked_example"`
	EvidenceRefs       []string   `json:"evidence_refs" db:"evidence_refs"`
	VerificationPlan   string     `json:"verification_plan" db:"verification_plan"`
	EstimatedTokens    *int32     `json:"estimated_tokens" db:"estimated_tokens"`
	EstimatedCostUSD   *float64   `json:"estimated_cost_usd" db:"estimated_cost_usd"`
	ApproachApprovedBy *string    `json:"approach_approved_by" db:"approach_approved_by"`
	ApproachApprovedAt *time.Time `json:"approach_approved_at" db:"approach_approved_at"`
	// Section C: what the AI reports and a machine checks. Resolution is the result.
	VerifiedBy      *string    `json:"verified_by" db:"verified_by"`
	MeasuredDelta   *string    `json:"measured_delta" db:"measured_delta"`
	GatePassed      *bool      `json:"gate_passed" db:"gate_passed"`
	GateCheckedAt   *time.Time `json:"gate_checked_at" db:"gate_checked_at"`
	SummaryForHuman *string    `json:"summary_for_human" db:"summary_for_human"`
	// DecisionID is the ticket's current decision (TP-42); the rest are its history.
	DecisionID *uuid.UUID `json:"decision_id" db:"decision_id"`
	// Goals are the key goals (1-9) the ticket serves (TP-107).
	Goals []int32 `json:"goals" db:"goals"`
}

// TicketColumns is every column of tickets except the generated search vector, in the order Scan
// expects. Kept next to the struct because the two must move together.
const TicketColumns = `id, project_id, number, key, title, description, type, priority, ` +
	`status, source, area_id, batch_id, batch_position, fingerprint, seen_count, last_seen_at, branch, ` +
	`resolution, claimed_by, claimed_at, version, created_at, updated_at, completed_at, ` +
	`intent, constraints, non_goals, approach, risk_and_rollback, worked_example, evidence_refs, ` +
	`verification_plan, estimated_tokens, estimated_cost_usd, approach_approved_by, approach_approved_at, ` +
	`verified_by, measured_delta, gate_passed, gate_checked_at, summary_for_human, decision_id, goals`

// CriterionTier is how a success criterion is checked: the human runs it, reads a number, or a gate
// command's exit code decides.
type CriterionTier string

const (
	TierBehavioural CriterionTier = "behavioural"
	TierMeasurement CriterionTier = "measurement"
	TierGate        CriterionTier = "gate"
)

var CriterionTiers = []CriterionTier{TierBehavioural, TierMeasurement, TierGate}

type TicketCriterion struct {
	ID         uuid.UUID     `json:"id" db:"id"`
	TicketID   uuid.UUID     `json:"ticket_id" db:"ticket_id"`
	Tier       CriterionTier `json:"tier" db:"tier"`
	Ordinal    int32         `json:"ordinal" db:"ordinal"`
	Text       string        `json:"text" db:"text"`
	TripwireID *uuid.UUID    `json:"tripwire_id" db:"tripwire_id"`
	CreatedAt  time.Time     `json:"created_at" db:"created_at"`
}

// Verdict is a judgement on one success criterion of a finished ticket.
type Verdict string

const (
	VerdictPass      Verdict = "pass"
	VerdictFail      Verdict = "fail"
	VerdictDeviation Verdict = "deviation"
)

var Verdicts = []Verdict{VerdictPass, VerdictFail, VerdictDeviation}

func ParseVerdict(raw string) (Verdict, error) { return parseEnum(raw, Verdicts, "Verdict") }

// CriterionVerdict is never edited; the newest per criterion is the one that counts.
type CriterionVerdict struct {
	ID          uuid.UUID `json:"id" db:"id"`
	CriterionID uuid.UUID `json:"criterion_id" db:"criterion_id"`
	Verdict     Verdict   `json:"verdict" db:"verdict"`
	Reason      string    `json:"reason" db:"reason"`
	Evidence    []string  `json:"evidence" db:"evidence"`
	JudgedBy    string    `json:"judged_by" db:"judged_by"`
	CreatedAt   time.Time `json:"created_at" db:"created_at"`
}

// Tripwire is a test whose meaning a human has confirmed. A new BodySHA256 clears the confirmation.
type Tripwire struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	ProjectID   uuid.UUID  `json:"project_id" db:"project_id"`
	TestRef     string     `json:"test_ref" db:"test_ref"`
	Meaning     string     `json:"meaning" db:"meaning"`
	BodySHA256  string     `json:"body_sha256" db:"body_sha256"`
	ConfirmedBy *string    `json:"confirmed_by" db:"confirmed_by"`
	ConfirmedAt *time.Time `json:"confirmed_at" db:"confirmed_at"`
	CreatedAt   time.Time  `json:"created_at" db:"created_at"`
}

// TicketCheck is a public check: the gate tier an agent may read. Held-out checks are never loaded
// into this struct by anything a transport can reach.
type TicketCheck struct {
	ID          uuid.UUID `json:"id" db:"id"`
	TicketID    uuid.UUID `json:"ticket_id" db:"ticket_id"`
	Ordinal     int32     `json:"ordinal" db:"ordinal"`
	Description string    `json:"description" db:"description"`
	Kind        string    `json:"kind" db:"kind"`
	Command     *string   `json:"command" db:"command"`
	ExpectExit  int32     `json:"expect_exit" db:"expect_exit"`
}

// DecisionKind is what was decided about a ticket.
type DecisionKind string

const (
	DecisionAccepted DecisionKind = "accepted"
	DecisionDeferred DecisionKind = "deferred"
	DecisionRejected DecisionKind = "rejected"
	DecisionReframed DecisionKind = "reframed"
)

var DecisionKinds = []DecisionKind{DecisionAccepted, DecisionDeferred, DecisionRejected, DecisionReframed}

func ParseDecisionKind(raw string) (DecisionKind, error) {
	return parseEnum(raw, DecisionKinds, "DecisionKind")
}

// Decision is never edited: a changed mind is a new row that supersedes it. MadeBy is "human" or
// "ai", set by the transport the decision arrived on, never by the caller.
type Decision struct {
	ID              uuid.UUID    `json:"id" db:"id"`
	TicketID        uuid.UUID    `json:"ticket_id" db:"ticket_id"`
	Kind            DecisionKind `json:"kind" db:"kind"`
	MadeBy          string       `json:"made_by" db:"made_by"`
	EvidenceRefs    []string     `json:"evidence_refs" db:"evidence_refs"`
	Rationale       string       `json:"rationale" db:"rationale"`
	RevisitTrigger  *string      `json:"revisit_trigger" db:"revisit_trigger"`
	Supersedes      *uuid.UUID   `json:"supersedes" db:"supersedes"`
	CitesRevisionID *uuid.UUID   `json:"cites_revision_id" db:"cites_revision_id"`
	Actor           string       `json:"actor" db:"actor"`
	CreatedAt       time.Time    `json:"created_at" db:"created_at"`
}

type TicketLocation struct {
	ID        uuid.UUID `json:"id" db:"id"`
	TicketID  uuid.UUID `json:"ticket_id" db:"ticket_id"`
	FilePath  string    `json:"file_path" db:"file_path"`
	LineStart *int32    `json:"line_start" db:"line_start"`
	LineEnd   *int32    `json:"line_end" db:"line_end"`
	Snippet   *string   `json:"snippet" db:"snippet"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

type PullRequest struct {
	ID        uuid.UUID        `json:"id" db:"id"`
	TicketID  uuid.UUID        `json:"ticket_id" db:"ticket_id"`
	URL       string           `json:"url" db:"url"`
	Number    *int32           `json:"number" db:"number"`
	Branch    *string          `json:"branch" db:"branch"`
	State     PullRequestState `json:"state" db:"state"`
	CreatedAt time.Time        `json:"created_at" db:"created_at"`
	UpdatedAt time.Time        `json:"updated_at" db:"updated_at"`
}

type Comment struct {
	ID        uuid.UUID `json:"id" db:"id"`
	TicketID  uuid.UUID `json:"ticket_id" db:"ticket_id"`
	Author    string    `json:"author" db:"author"`
	Body      string    `json:"body" db:"body"`
	Kind      string    `json:"kind" db:"kind"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

type TicketEvent struct {
	ID        int64     `json:"id" db:"id"`
	TicketID  uuid.UUID `json:"ticket_id" db:"ticket_id"`
	Actor     string    `json:"actor" db:"actor"`
	Field     string    `json:"field" db:"field"`
	OldValue  *string   `json:"old_value" db:"old_value"`
	NewValue  *string   `json:"new_value" db:"new_value"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

// GuidanceKind is what a document is for. The kind decides where it sits in a bundle: an agent
// should read the workflow before the area's rules, and the rules before a checklist.
type GuidanceKind string

const (
	GuidanceWorkflow   GuidanceKind = "workflow"
	GuidanceConvention GuidanceKind = "convention"
	GuidanceArea       GuidanceKind = "area"
	GuidanceChecklist  GuidanceKind = "checklist"
)

var GuidanceKinds = []GuidanceKind{GuidanceWorkflow, GuidanceConvention, GuidanceArea, GuidanceChecklist}

func ParseGuidanceKind(raw string) (GuidanceKind, error) {
	return parseEnum(raw, GuidanceKinds, "GuidanceKind")
}

// GuidanceDoc is mutable; its revisions are not. Editing appends a revision and repoints
// CurrentRevisionID, so what an agent was served can be pinned by foreign key rather than by
// promise.
type GuidanceDoc struct {
	ID                 uuid.UUID    `json:"id" db:"id"`
	ProjectID          uuid.UUID    `json:"project_id" db:"project_id"`
	Slug               string       `json:"slug" db:"slug"`
	Title              string       `json:"title" db:"title"`
	Kind               GuidanceKind `json:"kind" db:"kind"`
	CurrentRevisionID  *uuid.UUID   `json:"current_revision_id" db:"current_revision_id"`
	Inline             bool         `json:"inline" db:"inline"`
	Weight             int32        `json:"weight" db:"weight"`
	SourcePaths        []string     `json:"source_paths" db:"source_paths"`
	SourceCommit       *string      `json:"source_commit" db:"source_commit"`
	ReviewedAt         *time.Time   `json:"reviewed_at" db:"reviewed_at"`
	ReviewedBy         *string      `json:"reviewed_by" db:"reviewed_by"`
	ReviewIntervalDays int32        `json:"review_interval_days" db:"review_interval_days"`
	Stale              bool         `json:"stale" db:"stale"`
	StaleReason        *string      `json:"stale_reason" db:"stale_reason"`
	DriftReportedAt    *time.Time   `json:"drift_reported_at" db:"drift_reported_at"`
	ArchivedAt         *time.Time   `json:"archived_at" db:"archived_at"`
	Version            int32        `json:"version" db:"version"`
	CreatedAt          time.Time    `json:"created_at" db:"created_at"`
	UpdatedAt          time.Time    `json:"updated_at" db:"updated_at"`
}

type GuidanceRevision struct {
	ID            uuid.UUID `json:"id" db:"id"`
	DocumentID    uuid.UUID `json:"document_id" db:"document_id"`
	Revision      int32     `json:"revision" db:"revision"`
	Body          string    `json:"body" db:"body"`
	ContentSHA256 string    `json:"content_sha256" db:"content_sha256"`
	Author        string    `json:"author" db:"author"`
	CreatedAt     time.Time `json:"created_at" db:"created_at"`
}

// ServedGuidance is one document as an agent receives it. A document over the claim's budget still
// appears, with Body empty and Fetch naming the call that gets it — being told what you did not
// receive is far more use than silence.
type ServedGuidance struct {
	Slug     string       `json:"slug"`
	Title    string       `json:"title"`
	Kind     GuidanceKind `json:"kind"`
	Revision int32        `json:"revision"`
	Stale    bool         `json:"stale,omitempty"`
	Tokens   int32        `json:"tokens"`
	Body     string       `json:"body,omitempty"`
	Omitted  string       `json:"omitted,omitempty"`
	Fetch    string       `json:"fetch,omitempty"`
}

// GuidanceBundle is everything served with one claim, plus the arithmetic that decided it.
type GuidanceBundle struct {
	BudgetTokens int32            `json:"budget_tokens"`
	UsedTokens   int32            `json:"used_tokens"`
	Docs         []ServedGuidance `json:"docs"`
	Omitted      []ServedGuidance `json:"omitted,omitempty"`
	// SHA256 over document:revision:content per served document, in order. What pins a claim to
	// exactly the bytes it was given.
	SHA256 string `json:"sha256,omitempty"`
}

// --- views ------------------------------------------------------------------

// TicketView is a ticket plus the two derived fields every transport exposes.
type TicketView struct {
	Ticket Ticket
	Area   *Area
	// PRURL is the url of the most recent pull request, if any.
	PRURL *string
	// Tags are names from the project's managed tag list, sorted.
	Tags []string
}

type AreaRef struct {
	ID   uuid.UUID `json:"id" db:"id"`
	Name string    `json:"name" db:"name"`
}

// TicketJSON is the REST/MCP shape: the flat ticket row plus area and pr_url. The embedded Ticket
// is anonymous, which is Go's serde(flatten): its fields encode inline rather than under a key.
// transitions is rule 4: the allowed moves, and nothing else.
var transitions = map[Status][]Status{
	StatusTriage:     {StatusOpen, StatusClosed},
	StatusOpen:       {StatusInProgress, StatusClosed},
	StatusInProgress: {StatusOpen, StatusDone, StatusClosed},
	StatusDone:       {StatusOpen},
	StatusClosed:     {StatusOpen},
}

// NextStatuses is one row of the transition table: where a ticket in `from` may go. Served on every
// ticket so a client offers exactly these moves instead of keeping its own copy (TP-27). Leaving
// triage also needs the human's transport (rule 17), which is the caller's to know.
func NextStatuses(from Status) []Status {
	return append([]Status{}, transitions[from]...)
}

type TicketJSON struct {
	Ticket
	Area  *AreaRef `json:"area" db:"area"`
	PRURL *string  `json:"pr_url" db:"pr_url"`
	Tags  []string `json:"tags" db:"tags"`
	// The moves this ticket can make from its current status (rule 4).
	NextStatuses []Status `json:"next_statuses" db:"-"`
}

func (v *TicketView) ToJSON() TicketJSON {
	tags := v.Tags
	if tags == nil {
		tags = []string{}
	}
	out := TicketJSON{Ticket: v.Ticket, PRURL: v.PRURL, Tags: tags, NextStatuses: NextStatuses(v.Ticket.Status)}
	if v.Area != nil {
		out.Area = &AreaRef{ID: v.Area.ID, Name: v.Area.Name}
	}
	return out
}

// TicketPageJSON is the GET /api/tickets and list_tickets response envelope.
type TicketPageJSON struct {
	Items []TicketJSON `json:"items" db:"items"`
	Total int64        `json:"total" db:"total"`
}

// Ptr is the shorthand for the many optional columns; Go has no Some().
func Ptr[T any](v T) *T { return &v }

// Deref reads through an optional column, giving the zero value for NULL.
func Deref[T any](v *T) T {
	if v == nil {
		var zero T
		return zero
	}
	return *v
}

// NonNil renders an empty collection as `[]`, not `null`: a nil Go slice marshals to null and a
// client reading `.criteria.length` crashes on a ticket that has none. Four copies before TP-293.
func NonNil[T any](xs []T) []T {
	if xs == nil {
		return []T{}
	}
	return xs
}

func (t Ticket) String() string { return fmt.Sprintf("%s (%s)", t.Key, t.Status) }
