package model

import (
	"fmt"
	"io"
	"strconv"
)

// The four enums are GraphQL enums as well as Postgres text and JSON strings. Implementing
// gqlgen's marshaller here — rather than letting gqlgen generate a parallel set of enum types —
// keeps one definition of "the statuses" in the codebase and saves a conversion at every boundary.
// It is the only place the model knows a transport exists.

func marshalEnum[T ~string](w io.Writer, v T) {
	_, _ = io.WriteString(w, strconv.Quote(string(v)))
}

func unmarshalEnum[T ~string](v any, allowed []T, name string) (T, error) {
	var zero T
	raw, ok := v.(string)
	if !ok {
		return zero, fmt.Errorf("%s must be a string", name)
	}
	return parseEnum(raw, allowed, name)
}

func (e TicketType) MarshalGQL(w io.Writer) { marshalEnum(w, e) }
func (e *TicketType) UnmarshalGQL(v any) (err error) {
	*e, err = unmarshalEnum(v, TicketTypes, "TicketType")
	return
}

func (e Priority) MarshalGQL(w io.Writer) { marshalEnum(w, e) }
func (e *Priority) UnmarshalGQL(v any) (err error) {
	*e, err = unmarshalEnum(v, Priorities, "Priority")
	return
}

func (e Status) MarshalGQL(w io.Writer) { marshalEnum(w, e) }
func (e *Status) UnmarshalGQL(v any) (err error) {
	*e, err = unmarshalEnum(v, Statuses, "Status")
	return
}

func (e Source) MarshalGQL(w io.Writer) { marshalEnum(w, e) }
func (e *Source) UnmarshalGQL(v any) (err error) {
	*e, err = unmarshalEnum(v, Sources, "Source")
	return
}

func (e BatchKind) MarshalGQL(w io.Writer) { marshalEnum(w, e) }
func (e *BatchKind) UnmarshalGQL(v any) (err error) {
	*e, err = unmarshalEnum(v, BatchKinds, "BatchKind")
	return
}

func (e GuidanceKind) MarshalGQL(w io.Writer) { marshalEnum(w, e) }
func (e *GuidanceKind) UnmarshalGQL(v any) (err error) {
	*e, err = unmarshalEnum(v, GuidanceKinds, "GuidanceKind")
	return
}

func (e CheckpointDecision) MarshalGQL(w io.Writer) { marshalEnum(w, e) }
func (e *CheckpointDecision) UnmarshalGQL(v any) (err error) {
	*e, err = unmarshalEnum(v, CheckpointDecisions, "CheckpointDecision")
	return
}

func (e CriterionTier) MarshalGQL(w io.Writer) { marshalEnum(w, e) }
func (e *CriterionTier) UnmarshalGQL(v any) (err error) {
	*e, err = unmarshalEnum(v, CriterionTiers, "CriterionTier")
	return
}

func (e DecisionKind) MarshalGQL(w io.Writer) { marshalEnum(w, e) }
func (e *DecisionKind) UnmarshalGQL(v any) (err error) {
	*e, err = unmarshalEnum(v, DecisionKinds, "DecisionKind")
	return
}

func (e Verdict) MarshalGQL(w io.Writer) { marshalEnum(w, e) }
func (e *Verdict) UnmarshalGQL(v any) (err error) {
	*e, err = unmarshalEnum(v, Verdicts, "Verdict")
	return
}
