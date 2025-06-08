package types

import (
	"encoding/json"
	"fmt"
)

// UnmarshalJSON implements a custom unmarshaler for MatchExpression to handle value
func (m *MatchExpression) UnmarshalJSON(data []byte) error {
	// 1. Define an alias type to avoid an infinite recursion loop.
	//    If we called json.Unmarshal on MatchExpression inside this method,
	//    it would call this method again (stack overflow).
	type alias MatchExpression

	// 2. Create a temporary struct to unmarshal into. value needs to be raw
	temp := &struct {
		Value json.RawMessage `json:"value"`
		*alias
	}{
		// 3. Point the alias to the current MatchExpression instance ('m')
		//    so that Op and Key are populated directly into it.
		alias: (*alias)(m),
	}

	// 4. Unmarshal the data into our temporary struct.
	if err := json.Unmarshal(data, &temp); err != nil {
		return fmt.Errorf("failed to unmarshal match expression: %w", err)
	}

	// 5. Now, inspect the raw JSON for the 'value' field.
	if len(temp.Value) > 0 {
		// If the first character is '[', it's a JSON array.
		if temp.Value[0] == '[' {
			// Unmarshal it as a normal slice of strings.
			if err := json.Unmarshal(temp.Value, &m.Value); err != nil {
				return fmt.Errorf("failed to unmarshal 'value' as array: %w", err)
			}
		} else {
			// Otherwise, assume it's a single JSON string.
			var strValue string
			if err := json.Unmarshal(temp.Value, &strValue); err != nil {
				return fmt.Errorf("failed to unmarshal 'value' as string: %w", err)
			}
			// **This is the key step:** We normalize the single string
			// into a slice containing just that string.
			m.Value = []string{strValue}
		}
	}

	return nil
}
