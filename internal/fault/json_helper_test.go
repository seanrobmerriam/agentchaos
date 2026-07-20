package fault_test

import "encoding/json"

// jsonUnmarshal wraps encoding/json.Unmarshal to avoid importing it in
// every test file.
func jsonUnmarshal(b []byte, v any) error {
	return json.Unmarshal(b, v)
}