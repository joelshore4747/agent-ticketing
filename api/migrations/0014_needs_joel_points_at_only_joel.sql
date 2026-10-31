-- TP-244: the needs-joel description restated the moves only Joel makes, in words that disagreed
-- with the four other copies of that list ("credentials, spending, a decision" here, "credentials,
-- spending, anything irreversible" in the-agents, "accounts" too in approver.md). The list now lives
-- once, as the "Only Joel" section of the-agents guidance, and every other place points at it.
--
-- Migrations are history and are not edited, so 0011's text is corrected forward rather than in
-- place, for the projects already carrying it. service.starterTags holds the same string;
-- TestNewProjectGetsTheStarterTags compares a created project against the migrated one and fails if
-- the two drift apart. Matching on the old description leaves a hand-edited one alone.
UPDATE tags
SET description = 'Waiting on a move on the Only Joel list (the-agents guidance). The loop stops while any open ticket carries it.'
WHERE name = 'needs-joel'
  AND description = 'Waiting on something only Joel can do (credentials, spending, a decision). The loop stops while any open ticket carries it.';
