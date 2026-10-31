package service

import (
	"sort"

	"github.com/joelshore/ticketing/api/internal/apperr"
)

// KeyGoals is how many key goals there are. Their text is the Self-improving tracker roadmap's spec
// and the key-goals guidance document; tickets carry only the numbers (TP-107).
const KeyGoals = 9

// normaliseGoals sorts and de-duplicates goal numbers, refusing any that is not a key goal.
func normaliseGoals(in []int32) ([]int32, error) {
	seen := map[int32]bool{}
	out := []int32{}
	for _, g := range in {
		if g < 1 || g > KeyGoals {
			return nil, apperr.BadRequestf("goal %d is not a key goal; there are %d (see the key-goals guidance)", g, KeyGoals)
		}
		if !seen[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}
