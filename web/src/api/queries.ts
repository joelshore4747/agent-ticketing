// GraphQL documents as string constants. Field sets are hand-tuned to what each
// view needs (list rows vs. the full ticket detail) since there is no codegen.

const PROJECT_FIELDS = /* GraphQL */ `
  id
  key
  name
`;

export const PROJECTS_QUERY = /* GraphQL */ `
  query Projects {
    projects {
      ${PROJECT_FIELDS}
    }
  }
`;

export const CREATE_PROJECT_MUTATION = /* GraphQL */ `
  mutation CreateProject($key: String!, $name: String!) {
    createProject(key: $key, name: $name) {
      ${PROJECT_FIELDS}
    }
  }
`;

const TICKET_LIST_FIELDS = /* GraphQL */ `
  id
  key
  number
  title
  type
  priority
  status
  nextStatuses
  source
  area {
    id
    name
  }
  resolution
  claimedBy
  claimedAt
  batchPosition
  tags
  goals
  summaryForHuman
  version
  createdAt
  updatedAt
`;

export const TICKETS_QUERY = /* GraphQL */ `
  query Tickets($project: String, $filter: TicketFilter, $search: String, $limit: Int, $offset: Int) {
    tickets(project: $project, filter: $filter, search: $search, limit: $limit, offset: $offset) {
      total
      items {
        ${TICKET_LIST_FIELDS}
      }
    }
  }
`;

export const TICKET_QUERY = /* GraphQL */ `
  query Ticket($project: String, $key: String!) {
    ticket(project: $project, key: $key) {
      id
      key
      number
      title
      description
      type
      priority
      status
      nextStatuses
      source
      branch
      prUrl
      resolution
      fingerprint
      seenCount
      claimedBy
      claimedAt
      version
      createdAt
      updatedAt
      completedAt
      tags
      goals
      summaryForHuman
      intent
      constraints
      nonGoals
      criteria {
        id
        tier
        ordinal
        text
        tripwire {
          id
          testRef
          meaning
          confirmedBy
          confirmedAt
        }
        verdicts {
          id
          verdict
          reason
          evidence
          judgedBy
          createdAt
        }
      }
      checks {
        id
        ordinal
        description
        kind
        command
        expectExit
      }
      decisionId
      decisions {
        id
        kind
        madeBy
        evidenceRefs
        rationale
        revisitTrigger
        supersedes
        citesRevisionId
        actor
        createdAt
      }
      approach
      riskAndRollback
      workedExample
      verificationPlan
      evidenceRefs
      estimatedTokens
      estimatedCostUsd
      approachApprovedBy
      approachApprovedAt
      verifiedBy
      measuredDelta
      gatePassed
      gateCheckedAt
      area {
        id
        name
      }
      comments {
        id
        author
        body
        kind
        createdAt
      }
      events {
        id
        actor
        field
        oldValue
        newValue
        createdAt
      }
      locations {
        id
        filePath
        lineStart
        lineEnd
        snippet
      }
      pullRequests {
        id
        url
        number
        branch
        state
        createdAt
      }
    }
  }
`;

export const AREAS_QUERY = /* GraphQL */ `
  query Areas($project: String, $includeArchived: Boolean) {
    areas(project: $project, includeArchived: $includeArchived) {
      id
      name
      description
      pathGlobs
      autoAcceptAi
      archivedAt
      createdAt
      updatedAt
      openCount
    }
  }
`;

export const CREATE_TICKET_MUTATION = /* GraphQL */ `
  mutation CreateTicket($project: String, $input: CreateTicketInput!) {
    createTicket(project: $project, input: $input) {
      ${TICKET_LIST_FIELDS}
    }
  }
`;

export const UPDATE_TICKET_MUTATION = /* GraphQL */ `
  mutation UpdateTicket($id: ID!, $version: Int!, $patch: UpdateTicketInput!) {
    updateTicket(id: $id, version: $version, patch: $patch) {
      ${TICKET_LIST_FIELDS}
      description
      resolution
    }
  }
`;

export const TRANSITION_TICKET_MUTATION = /* GraphQL */ `
  mutation TransitionTicket($id: ID!, $version: Int!, $status: Status!, $resolution: String) {
    transitionTicket(id: $id, version: $version, status: $status, resolution: $resolution) {
      ${TICKET_LIST_FIELDS}
      resolution
      completedAt
    }
  }
`;

export const ACCEPT_TICKET_MUTATION = /* GraphQL */ `
  mutation AcceptTicket($id: ID!, $version: Int!) {
    acceptTicket(id: $id, version: $version) {
      ${TICKET_LIST_FIELDS}
    }
  }
`;

export const DISMISS_TICKET_MUTATION = /* GraphQL */ `
  mutation DismissTicket($id: ID!, $version: Int!, $resolution: String) {
    dismissTicket(id: $id, version: $version, resolution: $resolution) {
      ${TICKET_LIST_FIELDS}
      resolution
    }
  }
`;

export const ADD_COMMENT_MUTATION = /* GraphQL */ `
  mutation AddComment($ticketId: ID!, $body: String!) {
    addComment(ticketId: $ticketId, body: $body) {
      id
      author
      body
      kind
      createdAt
    }
  }
`;

export const CREATE_AREA_MUTATION = /* GraphQL */ `
  mutation CreateArea($project: String, $input: AreaInput!) {
    createArea(project: $project, input: $input) {
      id
      name
      description
      pathGlobs
      autoAcceptAi
      archivedAt
      createdAt
      updatedAt
      openCount
    }
  }
`;

export const UPDATE_AREA_MUTATION = /* GraphQL */ `
  mutation UpdateArea($project: String, $id: ID!, $patch: AreaPatch!) {
    updateArea(project: $project, id: $id, patch: $patch) {
      id
      name
      description
      pathGlobs
      autoAcceptAi
      archivedAt
      createdAt
      updatedAt
      openCount
    }
  }
`;

export const ARCHIVE_AREA_MUTATION = /* GraphQL */ `
  mutation ArchiveArea($project: String, $id: ID!) {
    archiveArea(project: $project, id: $id) {
      id
      archivedAt
    }
  }
`;

export const UNARCHIVE_AREA_MUTATION = /* GraphQL */ `
  mutation UnarchiveArea($project: String, $id: ID!) {
    unarchiveArea(project: $project, id: $id) {
      id
      archivedAt
    }
  }
`;

const BATCH_FIELDS = /* GraphQL */ `
  id
  kind
  actor
  title
  what
  how
  brief
  gitRef
  summary
  findingsCount
  startedAt
  finishedAt
  roadmapId
  sequence
  checkpointDecision
  checkpointNote
  checkpointBy
  checkpointAt
  digest
  digestAt
  reviewsRequired
`;

const GUIDANCE_FIELDS = /* GraphQL */ `
  id
  slug
  title
  kind
  inline
  weight
  sourcePaths
  sourceCommit
  reviewedAt
  reviewedBy
  stale
  staleReason
  driftReportedAt
  updatedAt
`;

// Bodies included. The whole corpus is a handful of documents and a few KB; paying a round trip
// per document to save that would be the more expensive mistake.
export const GUIDANCE_QUERY = /* GraphQL */ `
  query Guidance($project: String) {
    guidance(project: $project) {
      ${GUIDANCE_FIELDS}
      body
      revision
    }
  }
`;

export const BATCHES_QUERY = /* GraphQL */ `
  query Batches($project: String) {
    batches(project: $project) {
      ${BATCH_FIELDS}
    }
  }
`;

export const BATCH_QUERY = /* GraphQL */ `
  query Batch($project: String, $id: ID!) {
    batch(project: $project, id: $id) {
      ${BATCH_FIELDS}
      guidance {
        id
        slug
        title
        kind
        body
        revision
        inline
        weight
        sourcePaths
        sourceCommit
        reviewedAt
        reviewedBy
        stale
        staleReason
        driftReportedAt
        updatedAt
      }
      reviews {
        id
        phase
        commit
        body
        actor
        createdAt
      }
      tickets {
        ${TICKET_LIST_FIELDS}
      }
      progress {
        next
        lockedBy
        blockedBy {
          key
          behind
        }
      }
    }
  }
`;

export const ROADMAPS_QUERY = /* GraphQL */ `
  query Roadmaps($project: String) {
    roadmaps(project: $project) {
      id
      title
      spec
      createdAt
      archivedAt
      batches {
        state
        work
        workDone
        batch {
          ${BATCH_FIELDS}
        }
      }
    }
  }
`;

export const TAGS_QUERY = /* GraphQL */ `
  query Tags($project: String) {
    tags(project: $project) {
      id
      name
      description
      archivedAt
      createdAt
    }
  }
`;

// The human's two moves on a plan. The agent's transports do not carry them.
export const ACCEPT_BATCH_MUTATION = /* GraphQL */ `
  mutation AcceptBatch($project: String, $id: ID!) {
    acceptBatch(project: $project, id: $id) {
      id
    }
  }
`;

export const CHECKPOINT_BATCH_MUTATION = /* GraphQL */ `
  mutation CheckpointBatch($project: String, $id: ID!, $decision: CheckpointDecision!, $note: String) {
    checkpointBatch(project: $project, id: $id, decision: $decision, note: $note) {
      id
      checkpointDecision
    }
  }
`;

// The human's side of the ownership model; neither is on Claude's transports.
export const SET_FOUNDATION_MUTATION = /* GraphQL */ `
  mutation SetTicketFoundation($id: ID!, $version: Int!, $input: FoundationInput!) {
    setTicketFoundation(id: $id, version: $version, input: $input) {
      id
      version
    }
  }
`;

export const APPROVE_APPROACH_MUTATION = /* GraphQL */ `
  mutation ApproveApproach($id: ID!, $version: Int!) {
    approveApproach(id: $id, version: $version) {
      id
      version
    }
  }
`;

export const NEEDS_YOU_QUERY = /* GraphQL */ `
  query NeedsYou($project: String) {
    needsYou(project: $project) {
      total
      triage {
        ${TICKET_LIST_FIELDS}
        description
      }
      checkpoints {
        ${BATCH_FIELDS}
      }
      proposals {
        ticketKey
        ticketTitle
        field
        proposed
        actor
        createdAt
      }
      uncited {
        ticketKey
        ticketTitle
        decision {
          id
          kind
          madeBy
          evidenceRefs
          rationale
          revisitTrigger
          supersedes
          citesRevisionId
          actor
          createdAt
        }
      }
      tripwires {
        id
        testRef
        meaning
        confirmedBy
        confirmedAt
      }
    }
  }
`;

// Every ticket waiting on the human by its needs-joel tag, whatever its status: the to-do list on
// Needs you. needsYou carries only the ones still in triage, but an accepted one (open) is still
// Joel's to do.
export const NEEDS_JOEL_QUERY = /* GraphQL */ `
  query NeedsJoel($project: String) {
    tickets(project: $project, filter: { tag: "needs-joel", status: [triage, open, in_progress] }, limit: 200) {
      items {
        ${TICKET_LIST_FIELDS}
        description
      }
    }
  }
`;

export const CONFIRM_TRIPWIRE_MUTATION = /* GraphQL */ `
  mutation ConfirmTripwire($project: String, $id: ID!) {
    confirmTripwire(project: $project, id: $id) {
      id
      confirmedBy
      confirmedAt
    }
  }
`;
