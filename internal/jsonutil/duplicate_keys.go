/*
Package jsonutil contains strict JSON checks shared by security-sensitive config loaders.

The standard decoder accepts repeated object keys and replaces invalid UTF-8;
this helper rejects both ambiguities before typed decoding.
*/
package jsonutil

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// RejectDuplicateObjectKeys requires valid UTF-8 JSON with unique keys in every object.
func RejectDuplicateObjectKeys(data []byte) error {
	if !utf8.Valid(data) {
		return errors.New("JSON input must be valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := consumeValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("JSON must contain exactly one value")
	}
	return nil
}

func consumeValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object contains a non-string key")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("JSON object contains duplicate key %q", key)
			}
			seen[key] = struct{}{}
			if err := consumeValue(decoder); err != nil {
				return err
			}
		}
		closeToken, err := decoder.Token()
		if err != nil || closeToken != json.Delim('}') {
			return errors.New("JSON object is not properly closed")
		}
	case '[':
		for decoder.More() {
			if err := consumeValue(decoder); err != nil {
				return err
			}
		}
		closeToken, err := decoder.Token()
		if err != nil || closeToken != json.Delim(']') {
			return errors.New("JSON array is not properly closed")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}
