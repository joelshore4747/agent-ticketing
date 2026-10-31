-- The needs-joel tag (goal 8; TP-127). The continuous loop stops while any open ticket carries it:
-- it is how an agent says "only the human can do this" (credentials, spending, a decision). Seeded
-- like the starter tags in 0003, for every project that lacks it.
INSERT INTO tags (project_id, name, description)
SELECT p.id, 'needs-joel', 'Waiting on something only Joel can do (credentials, spending, a decision). The loop stops while any open ticket carries it.'
FROM projects p
WHERE NOT EXISTS (SELECT 1 FROM tags t WHERE t.project_id = p.id AND t.name = 'needs-joel');
