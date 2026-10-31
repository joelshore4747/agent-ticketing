// Hand-written types matching the GraphQL SDL in docs/api.md. No codegen.

export type TicketType = 'bug' | 'feature' | 'improvement' | 'chore' | 'research' | 'design' | 'refactor' | 'eval';
export type Priority = 'low' | 'medium' | 'high' | 'urgent';
export type Status = 'triage' | 'open' | 'in_progress' | 'done' | 'closed';
export type Source = 'ai' | 'human';

export interface Project {
  id: string;
  key: string;
  name: string;
}

export interface Area {
  id: string;
  name: string;
  description: string;
  pathGlobs: string[];
  autoAcceptAi: boolean;
  archivedAt: string | null;
  createdAt: string;
  updatedAt: string;
  openCount: number;
}

export interface Comment {
  id: string;
  author: string;
  body: string;
  kind: string;
  createdAt: string;
}

export interface TicketEvent {
  id: string;
  actor: string;
  field: string;
  oldValue: string | null;
  newValue: string | null;
  createdAt: string;
}

export interface TicketLocation {
  id: string;
  filePath: string;
  lineStart: number | null;
  lineEnd: number | null;
  snippet: string | null;
}

export interface PullRequest {
  id: string;
  url: string;
  number: number | null;
  branch: string | null;
  state: string;
  createdAt: string;
}

// Fields present depend on which query fetched the ticket (list queries fetch a
// subset; the detail query fetches everything). Optional fields reflect that;
// required fields are the ones every query selects.
export interface Ticket {
  id: string;
  key: string;
  number: number;
  title: string;
  description?: string;
  type: TicketType;
  priority: Priority;
  status: Status;
  /** The key goals (1-9) this ticket serves. */
  goals?: number[];
  /** Where the API says this ticket may move from its status (rule 4). */
  nextStatuses?: Status[];
  source: Source;
  area: Area | null;
  branch?: string | null;
  prUrl?: string | null;
  resolution?: string | null;
  fingerprint?: string | null;
  seenCount?: number;
  claimedBy?: string | null;
  claimedAt?: string | null;
  /** Order inside a plan batch; null outside one. */
  batchPosition?: number | null;
  /** Names from the project's managed tag list, sorted. */
  tags?: string[];
  // Section A, the human's: intent, constraints, non-goals and success criteria.
  intent?: string;
  constraints?: string;
  nonGoals?: string;
  criteria?: Criterion[];
  // Section B, Claude's proposal; approved by the human.
  approach?: string;
  riskAndRollback?: string;
  workedExample?: string;
  verificationPlan?: string;
  evidenceRefs?: string[];
  estimatedTokens?: number | null;
  estimatedCostUsd?: number | null;
  approachApprovedBy?: string | null;
  approachApprovedAt?: string | null;
  // Section C, what Claude reports and a machine checks.
  verifiedBy?: string | null;
  measuredDelta?: string | null;
  gatePassed?: boolean | null;
  gateCheckedAt?: string | null;
  summaryForHuman?: string | null;
  checks?: TicketCheck[];
  // What has been decided about it, newest first; decisionId is the current one (rule 19).
  decisions?: Decision[];
  decisionId?: string | null;
  version: number;
  createdAt: string;
  updatedAt: string;
  completedAt?: string | null;
  comments?: Comment[];
  events?: TicketEvent[];
  locations?: TicketLocation[];
  pullRequests?: PullRequest[];
}

export interface TicketPage {
  items: Ticket[];
  total: number;
}

export interface TicketFilter {
  source?: Source;
  status?: Status[];
  type?: TicketType;
  areaId?: string;
  priority?: Priority;
  /** RFC 3339 instants, inclusive lower bounds (docs/api.md `TicketFilter`). */
  createdAfter?: string;
  updatedAfter?: string;
  tag?: string;
}

export interface CreateTicketInput {
  title: string;
  description?: string;
  type: TicketType;
  priority?: Priority;
  source?: Source;
  areaId?: string;
  branch?: string;
}

export interface UpdateTicketInput {
  title?: string;
  description?: string;
  type?: TicketType;
  priority?: Priority;
  areaId?: string;
  clearArea?: boolean;
  branch?: string;
  prUrl?: string;
}

export interface AreaInput {
  name: string;
  description?: string;
  pathGlobs?: string[];
  autoAcceptAi?: boolean;
}

export interface AreaPatch {
  name?: string;
  description?: string;
  pathGlobs?: string[];
  autoAcceptAi?: boolean;
}

export type CriterionTier = 'behavioural' | 'measurement' | 'gate';

export type DecisionKind = 'accepted' | 'deferred' | 'rejected' | 'reframed';

/** Never edited: a changed mind is a new decision that supersedes it. */
export interface Decision {
  id: string;
  kind: DecisionKind;
  madeBy: 'human' | 'ai';
  evidenceRefs: string[];
  rationale: string;
  revisitTrigger: string | null;
  supersedes: string | null;
  citesRevisionId: string | null;
  actor: string;
  createdAt: string;
}

/** Claude's change to a field the human owns, not yet acted on. */
export interface PendingProposal {
  ticketKey: string;
  ticketTitle: string;
  field: string;
  proposed: string;
  actor: string;
  createdAt: string;
}

export interface UncitedDecision {
  decision: Decision;
  ticketKey: string;
  ticketTitle: string;
}

/** Everything waiting on a decision only the human can make. */
export interface NeedsYou {
  triage: Ticket[];
  checkpoints: Batch[];
  proposals: PendingProposal[];
  uncited: UncitedDecision[];
  tripwires: Tripwire[];
  total: number;
}

export interface Tripwire {
  id: string;
  testRef: string;
  meaning: string;
  confirmedBy: string | null;
  confirmedAt: string | null;
}

export type Verdict = 'pass' | 'fail' | 'deviation';

/** A judgement on one criterion of a finished ticket; the newest is the one that counts. */
export interface CriterionVerdict {
  id: string;
  verdict: Verdict;
  reason: string;
  evidence: string[];
  judgedBy: string;
  createdAt: string;
}

export interface Criterion {
  id: string;
  tier: CriterionTier;
  ordinal: number;
  text: string;
  tripwire: Tripwire | null;
  verdicts?: CriterionVerdict[];
}

/** A public check: the gate an agent may read. Held-out checks are never served. */
export interface TicketCheck {
  id: string;
  ordinal: number;
  description: string;
  kind: string;
  command: string | null;
  expectExit: number;
}

export interface CriterionInput {
  tier: CriterionTier;
  text: string;
  tripwireId?: string;
}

export interface FoundationInput {
  intent?: string;
  constraints?: string;
  nonGoals?: string;
  criteria?: CriterionInput[];
}

export type BatchKind = 'audit' | 'review' | 'prompt' | 'plan';
export type CheckpointDecision = 'continue' | 'adjust' | 'stop';
export type RoadmapState =
  | 'locked'
  | 'unplanned'
  | 'open'
  | 'at_eval'
  | 'awaiting_checkpoint'
  | 'finished'
  | 'stopped';
export type GuidanceKind = 'workflow' | 'convention' | 'area' | 'checklist';

// A guidance document, as the API serves it. The document is mutable and its revisions are not,
// so (slug, revision) names exact bytes: the pair an agent's claim was pinned to.
export interface GuidanceDoc {
  id: string;
  slug: string;
  title: string;
  kind: GuidanceKind;
  body: string;
  revision: number;
  inline: boolean;
  weight: number;
  sourcePaths: string[];
  sourceCommit: string | null;
  reviewedAt: string | null;
  reviewedBy: string | null;
  stale: boolean;
  staleReason: string | null;
  driftReportedAt: string | null;
  updatedAt: string;
}

// Provenance and intent for a group of tickets: which were produced together, and why. `guidance`
// and `tickets` cost a query each, so only the detail query asks for them.
/** The project memory at one commit, taken before or after a batch (goal 2). */
export interface ProjectReview {
  id: string;
  phase: 'before' | 'after';
  commit: string;
  body: string;
  actor: string;
  createdAt: string;
}

export interface Batch {
  id: string;
  kind: BatchKind;
  actor: string;
  title: string;
  /** The human read of a batch: what it builds, how, and why (brief). */
  what: string;
  how: string;
  brief: string;
  gitRef: string | null;
  summary: string | null;
  findingsCount: number;
  startedAt: string;
  finishedAt: string | null;
  roadmapId: string | null;
  sequence: number | null;
  checkpointDecision: CheckpointDecision | null;
  checkpointNote: string | null;
  checkpointBy: string | null;
  checkpointAt: string | null;
  /** Markdown computed when the batch finishes, from its tickets and decisions. Nobody writes it. */
  digest?: string | null;
  digestAt?: string | null;
  /** Planned since goal 2: reviewed before it starts and after it ends. */
  reviewsRequired?: boolean;
  reviews?: ProjectReview[];
  guidance?: GuidanceDoc[];
  tickets?: Ticket[];
  progress?: BatchProgress | null;
}

/** Where a plan batch stands: the ticket to work next, what locks the whole batch, and which
 *  earlier ticket each waiting one is behind. */
export interface BatchProgress {
  next: string | null;
  lockedBy: string | null;
  blockedBy: { key: string; behind: string }[];
}

export interface RoadmapBatch {
  batch: Batch;
  state: RoadmapState;
  work: number;
  workDone: number;
}

export interface Roadmap {
  id: string;
  title: string;
  spec: string;
  createdAt: string;
  archivedAt: string | null;
  batches: RoadmapBatch[];
}

export interface Tag {
  id: string;
  name: string;
  description: string;
  archivedAt: string | null;
  createdAt: string;
}

export const TICKET_TYPES: TicketType[] = ['bug', 'feature', 'improvement', 'chore', 'research', 'design', 'refactor', 'eval'];
export const PRIORITIES: Priority[] = ['low', 'medium', 'high', 'urgent'];
export const STATUSES: Status[] = ['triage', 'open', 'in_progress', 'done', 'closed'];
