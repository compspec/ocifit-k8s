package validator

import (
	"log"
	"regexp"
	"strconv"

	"ghcr.io/compspec/ocifit-k8s/pkg/types"
)

func evaluateRule(expression types.MatchExpression, nodeLabels map[string]string) bool {
	// Get the value of the label from the node
	nodeVal, ok := nodeLabels[expression.Key]

	switch expression.Op {
	case types.MatchOpExists:
		// Rule matches if the key exists in the node's labels.
		return ok

	case types.MatchOpDoesNotExist:
		// Rule matches if the key does NOT exist in the node's labels.
		return !ok

	case types.MatchOpIn:
		// Rule matches if the key exists AND its value is in the spec's value list.
		if !ok {
			return false // Key must exist to be "In".
		}
		for _, v := range expression.Value {
			if nodeVal == v {
				return true // Found a match.
			}
		}
		// If we get here, we did not find a match in the list.
		return false

	case types.MatchOpNotIn:
		// Rule matches if the key does not exist, OR if it exists but its value
		// is not in the spec's value list.
		if !ok {

			// Key doesn't exist, so it can't be "In" the forbidden set.
			return true
		}
		for _, v := range expression.Value {

			// Found a forbidden match.
			if nodeVal == v {
				return false
			}
		}
		// Did not find any forbidden values.
		return true

	case types.MatchOpInRegexp:
		// Rule matches if the key exists AND its value matches any of the provided regex patterns.
		if !ok {

			// Key must exist to match a regex.
			return false
		}
		for _, pattern := range expression.Value {
			re, err := regexp.Compile(pattern)
			if err != nil {
				// A malformed regex in the spec is a spec error, not a node error.
				// Log it and treat it as a non-match for this pattern.
				log.Printf("WARN: Invalid regexp in compatibility spec: '%s'. Error: %v\n", pattern, err)
				continue
			}
			// Found a regex match.
			if re.MatchString(nodeVal) {
				return true
			}
		}
		// No patterns matched.
		return false

	case types.MatchOpGt, types.MatchOpLt, types.MatchOpGte, types.MatchOpLte:
		// Rule matches if the key exists AND its integer value is > or < the spec's value.
		if !ok {

			// Key must exist for comparison.
			return false
		}
		if len(expression.Value) == 0 {
			log.Printf("WARN: Gt/Lt operator used with no value in spec for key '%s'", expression.Key)
			return false
		}
		specValStr := expression.Value[0]

		// Convert node value to an integer.
		nodeInt, err := strconv.Atoi(nodeVal)
		if err != nil {
			log.Printf("WARN: Could not convert node label value '%s' to int for Gt/Lt/Gte/Lte comparison. Key: %s", nodeVal, expression.Key)
			return false
		}

		// Convert spec value to an integer.
		specInt, err := strconv.Atoi(specValStr)
		if err != nil {
			log.Printf("WARN: Could not convert spec value '%s' to int for Gt/Lt/Gte/Lte comparison. Key: %s", specValStr, expression.Key)
			return false
		}

		// Perform the correct comparison.
		if expression.Op == types.MatchOpGte {
			return nodeInt >= specInt
		}
		if expression.Op == types.MatchOpGt {
			return nodeInt > specInt
		}
		if expression.Op == types.MatchOpLte {
			return nodeInt <= specInt
		}
		// Last comparison, Lte
		return nodeInt < specInt

	default:
		// If we encounter an operator we don't recognize, log it and fail the match.
		log.Printf("WARN: Unsupported MatchOp in spec: %s", expression.Op)
		return false
	}
}
