package apply

import "github.com/openfga/mapper/language"

// uroKey returns a key identifying a tuple by (user, relation, object) only,
// without condition or context — used to detect competing writes on the same relationship.
func uroKey(t language.Tuple) string {
	return t.User + "\x00" + t.Relation + "\x00" + t.Object
}

// diffKey returns a comparable string for set membership in diffFilter.
// Includes user, relation, object, condition, and context but NOT action,
// since diffFilter assigns actions based on the diff result.
func diffKey(t language.Tuple) string {
	t.Action = "" // strip action so it doesn't affect the key
	return t.Key()
}

// copyTupleWithAction returns a shallow copy of t with the given action.
func copyTupleWithAction(t language.Tuple, action language.TupleAction) language.Tuple {
	t.Action = action
	return t
}

// diffFilter computes the write/delete delta for a single filter's read results.
//
// For patch filters: toDelete = existing - desired, toWrite = desired - existing.
// For delete filters: toDelete = existing, toWrite = nil.
//
// Comparison includes condition and context (not just User, Relation, Object),
// so a condition change on the same tuple identity produces delete+write.
// Output tuples have their Action set to ActionWrite or ActionDelete accordingly.
func diffFilter(action language.TupleFilterAction, existing, desired []language.Tuple) (toWrite, toDelete []language.Tuple) {
	if action == language.FilterActionDelete {
		for _, t := range existing {
			toDelete = append(toDelete, copyTupleWithAction(t, language.ActionDelete))
		}
		return nil, toDelete
	}

	// Precompute keys to avoid redundant diffKey() calls (which include json.Marshal for context).
	existingKeys := make([]string, len(existing))
	existingSet := make(map[string]struct{}, len(existing))
	for i, t := range existing {
		k := diffKey(t)
		existingKeys[i] = k
		existingSet[k] = struct{}{}
	}

	desiredKeys := make([]string, len(desired))
	desiredSet := make(map[string]struct{}, len(desired))
	for i, t := range desired {
		k := diffKey(t)
		desiredKeys[i] = k
		desiredSet[k] = struct{}{}
	}

	for i, t := range desired {
		if _, found := existingSet[desiredKeys[i]]; !found {
			toWrite = append(toWrite, copyTupleWithAction(t, language.ActionWrite))
		}
	}

	for i, t := range existing {
		if _, found := desiredSet[existingKeys[i]]; !found {
			toDelete = append(toDelete, copyTupleWithAction(t, language.ActionDelete))
		}
	}

	return toWrite, toDelete
}
