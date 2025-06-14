package types

// Image Compatibility Spec Structs (from NFD)
// https://github.com/kubernetes-sigs/node-feature-discovery/blob/master/api/image-compatibility/v1alpha1/spec.go

const CompatibilitySpecMediaType = "application/vnd.oci.image.compatibilities.v1+json"

// GroupRule is a list of node feature rules.
type GroupRule struct {
	MatchFeatures []FeatureMatcher `json:"matchFeatures"`
}

// FeatureMatcher contains a list of MatchExpression.
type FeatureMatcher struct {
	MatchExpressions []MatchExpression `json:"matchExpressions"`
}

// MatchOp is the operator to be used for matching.
type MatchOp string

// Supported operators
const (
	MatchOpIn           MatchOp = "In"
	MatchOpNotIn        MatchOp = "NotIn"
	MatchOpInRegexp     MatchOp = "InRegexp"
	MatchOpExists       MatchOp = "Exists"
	MatchOpDoesNotExist MatchOp = "DoesNotExist"
	MatchOpGt           MatchOp = "Gt"  // Greater than
	MatchOpLt           MatchOp = "Lt"  // Less than
	MatchOpGte          MatchOp = "Gte" // Greater than or equal to
	MatchOpLte          MatchOp = "Lte" // Less than or equal to
)

// MatchExpression specifies a requirement for matching features.
type MatchExpression struct {
	Op    MatchOp  `json:"op"`
	Key   string   `json:"key"`
	Value []string `json:"value,omitempty"`
}

// CompatibilitySpec represents image compatibility metadata.
type CompatibilitySpec struct {
	Version         string          `json:"version"`
	Compatibilities []Compatibility `json:"compatibilities"`
}

// Compatibility represents one set of rules.
type Compatibility struct {
	Rules       []GroupRule `json:"rules"`
	Weight      int         `json:"weight,omitempty"`
	Tag         string      `json:"tag,omitempty"`
	Description string      `json:"description,omitempty"`
}
