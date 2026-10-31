-- Key goals on tickets (goal 1 and 5 of the "Self-improving tracker" roadmap; TP-107).
--
-- Joel's nine key goals decide which work is done and judge it afterwards. A ticket names the goals
-- it serves, by number, so a batch can say which goals it moved and the next batch can be chosen by
-- the ones left behind. The goals' text is the roadmap's spec and the `key-goals` guidance document.
ALTER TABLE tickets ADD COLUMN goals integer[] NOT NULL DEFAULT '{}'
  CONSTRAINT tickets_goals_known CHECK (goals <@ '{1,2,3,4,5,6,7,8,9}'::integer[]);
