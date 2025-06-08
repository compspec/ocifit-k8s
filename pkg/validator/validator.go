package validator

import (
	"fmt"
	"log"

	"ghcr.io/compspec/ocifit-k8s/pkg/types"
)

// Make it pretty :)
var (
	Reset   = "\033[0m"
	Red     = "\033[31m"
	Green   = "\033[32m"
	Yellow  = "\033[33m"
	Blue    = "\033[34m"
	Magenta = "\033[35m"
	Cyan    = "\033[36m"
	Gray    = "\033[37m"
	White   = "\033[97m"
)

// evaluateCompatibilitySpec finds the best matching compatibility set from the spec
// by evaluating its rules against the node's labels.
func EvaluateCompatibilitySpec(spec *types.CompatibilitySpec, nodeLabels map[string]string) (string, error) {
	var bestMatch *types.Compatibility
	maxWeight := -1

	// Loop through each top-level Compatibility set in the spec.
	for i, comp := range spec.Compatibilities {
		allRulesMatch := true
		log.Printf("Assessing compatibility of %s", comp.Tag)

		// Each Compatibility set can have multiple GroupRules
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
						// As soon as one expression fails, the entire Compatibility set is invalid.
						log.Printf(Red+"  FAILED %s"+Reset, expression)
						allRulesMatch = false
						break
					} else {
						log.Printf(Green+"  PASSED %s"+Reset, expression)
					}
				}
			}
		}

		// If after checking all the rules for this set, allRulesMatch is still true...
		if allRulesMatch {
			log.Printf("⭐ Found a matching compatibility set: '%s' (weight %d)\n", comp.Tag, comp.Weight)

			// Check if this match is better (has a higher weight) than any previous match we found.
			if comp.Weight > maxWeight {
				maxWeight = comp.Weight
				// Capture the pointer to the current item in the slice.
				// Note I haven't thought of how I'd want to use weight yet
				bestMatch = &spec.Compatibilities[i]
			}
		}
	}

	// After checking all compatibility sets, see if we found a winner.
	if bestMatch == nil {
		return "", fmt.Errorf("no matching compatibility rule found for the given node labels")
	}

	log.Printf("Selected best match: '%s' with highest weight %d\n", bestMatch.Tag, bestMatch.Weight)
	return bestMatch.Tag, nil
}
