/*
Package jsonutil tests strict JSON structure and duplicate object-key rejection.
*/
package jsonutil

import "testing"

func TestRejectDuplicateObjectKeys(t *testing.T) {
	for _, input := range []string{
		`{"a":1,"nested":{"b":2,"b":3}}`,
		`{"a":1,"\u0061":2}`,
		`{"a":1} {"b":2}`,
		`{"a":[1,2]`,
	} {
		if err := RejectDuplicateObjectKeys([]byte(input)); err == nil {
			t.Fatalf("invalid or ambiguous JSON accepted: %s", input)
		}
	}
	if err := RejectDuplicateObjectKeys([]byte{'{', '"', 'a', '"', ':', '"', 0xff, '"', '}'}); err == nil {
		t.Fatal("invalid UTF-8 JSON input was accepted")
	}
	for _, input := range []string{`{"a":1,"b":{"a":2}}`, `[1,{"a":2}]`, `null`} {
		if err := RejectDuplicateObjectKeys([]byte(input)); err != nil {
			t.Fatalf("valid JSON rejected: %s: %v", input, err)
		}
	}
}
