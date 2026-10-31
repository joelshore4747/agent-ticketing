-- Usage on claims (TP-45, consolidated in TP-73; built in TP-101).
--
-- What the session that worked a claim cost: tokens by kind, the wall time, and the CLI's own dollar
-- figure, which is an estimate (it does not reliably track the token counts) and so is nullable. An
-- external session-end hook reports these through POST /api/tickets/{key}/usage; the API never
-- instruments the agent. Per-batch figures are sums over the batch's claims, computed when read.
ALTER TABLE ticket_claims
  ADD COLUMN input_tokens                bigint CHECK (input_tokens >= 0),
  ADD COLUMN output_tokens               bigint CHECK (output_tokens >= 0),
  ADD COLUMN cache_creation_input_tokens bigint CHECK (cache_creation_input_tokens >= 0),
  ADD COLUMN cache_read_input_tokens     bigint CHECK (cache_read_input_tokens >= 0),
  ADD COLUMN cost_usd                    numeric(12,4) CHECK (cost_usd >= 0),
  ADD COLUMN wall_time_seconds           integer CHECK (wall_time_seconds >= 0),
  ADD COLUMN usage_reported_at           timestamptz;
