package types

import (
	"encoding/json"
	"fmt"
)

// ModelCompatibilitySpecMediaType is the reserved media type for this new specification.
const ModelCompatibilitySpecMediaType = "application/vnd.oci.image.model-compatibilities.v1+json"

// ModelCompatibilitySpec represents image compatibility metadata driven by ML models.
type ModelCompatibilitySpec struct {
	Version         string               `json:"version"`
	Compatibilities []ModelCompatibility `json:"compatibilities"`
}

// ModelCompatibility represents one set of rules, pointing to a model.
type ModelCompatibility struct {
	Tag         string      `json:"tag,omitempty"`
	Description string      `json:"description,omitempty"`
	Rules       []ModelRule `json:"rules"`
	Weight      int         `json:"weight,omitempty"`
}

// ModelRule contains a pointer to a machine learning model to be used for matching.
type ModelRule struct {
	MatchModel MatchModel `json:"matchModel"`
}

// MatchModel contains the specification for the model to use.
type MatchModel struct {
	Model ModelSpec `json:"model"`
}

// ModelSpec defines the properties of the machine learning model.
type ModelSpec struct {
	Type      string            `json:"type"`
	Platforms map[string]string `json:"platforms"`
	Direction string            `json:"direction"`
	Name      string            `json:"name"`
	Filename  string            `json:"filename"`
}

// UnmarshalModelCompatibilitySpec is a helper function to parse a byte slice into a ModelCompatibilitySpec.
func UnmarshalModelCompatibilitySpec(data []byte) (*ModelCompatibilitySpec, error) {
	var spec ModelCompatibilitySpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("failed to unmarshal model compatibility spec: %w", err)
	}
	return &spec, nil
}
