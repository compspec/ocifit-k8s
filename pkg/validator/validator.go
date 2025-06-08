package validator

import (
	"fmt"
	"log"

	"ghcr.io/compspec/ocifit-k8s/pkg/types"
)

// evaluateCompatibilitySpec finds the best matching compatibility set from the spec
// by evaluating its rules against the node's labels.
// evaluateCompatibilitySpec finds the best matching compatibility set from the spec
// by evaluating its rules against the node's labels.
func EvaluateCompatibilitySpec(spec *types.CompatibilitySpec, nodeLabels map[string]string) (string, error) {
	var bestMatch *types.Compatibility
	maxWeight := -1

	// Loop through each top-level 'Compatibility' set in the spec.
	for i, comp := range spec.Compatibilities {
		allRulesMatch := true

		// Each 'Compatibility' set can have multiple 'GroupRule's.
		// All GroupRules must match for the set to be considered a match (AND logic).
		for _, groupRule := range comp.Rules {
			if !allRulesMatch {
				break
			}
			for _, featureMatcher := range groupRule.MatchFeatures {
				if !allRulesMatch {
					break
				}

				// Each FeatureMatcher has a slice of MatchExpressions. Loop through them.
				// All MatchExpressions must match (AND logic).
				for _, expression := range featureMatcher.MatchExpressions {
					if !evaluateRule(expression, nodeLabels) {
						// As soon as one expression fails, the entire 'Compatibility' set is invalid.
						allRulesMatch = false
						break
					}
				}
			}
		}

		// If, after checking all the rules for this 'Compatibility' set, allRulesMatch is still true...
		if allRulesMatch {
			log.Printf("Found a matching compatibility set: '%s' (weight %d)", comp.Tag, comp.Weight)

			// Check if this match is better (has a higher weight) than any previous match we found.
			if comp.Weight > maxWeight {
				maxWeight = comp.Weight
				// Capture the pointer to the current item in the slice.
				bestMatch = &spec.Compatibilities[i]
			}
		}
	}

	// After checking all compatibility sets, see if we found a winner.
	if bestMatch == nil {
		return "", fmt.Errorf("no matching compatibility rule found for the given node labels")
	}

	log.Printf("Selected best match: '%s' with highest weight %d", bestMatch.Tag, bestMatch.Weight)
	return bestMatch.Tag, nil
}
