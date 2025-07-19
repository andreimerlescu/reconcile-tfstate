package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	gover "github.com/hashicorp/go-version"
)

//go:embed VERSION
var versionBytes embed.FS

var currentVersion string

func Version() string {
	if len(currentVersion) == 0 {
		versionBytes, err := versionBytes.ReadFile("VERSION")
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			return "v0.0.0"
		}
		currentVersion = strings.TrimSpace(string(versionBytes))
	}
	return currentVersion
}

// sniffJSONStateVersion parses []byte from the state file and returns the version as a uint64 or an error
func sniffJSONStateVersion(src []byte) (uint64, error) {
	type VersionSniff struct {
		Version *uint64 `json:"version"`
	}
	var sniff VersionSniff
	err := json.Unmarshal(src, &sniff)
	if err != nil {
		if errors.Is(err, &json.SyntaxError{}) {
			var e *json.SyntaxError
			if ok := errors.As(err, &e); ok {
				return 0, fmt.Errorf("the state file could not be parsed as JSON: syntax error at byte offset %d", e.Offset)
			}
			return 0, fmt.Errorf("the state file could not be parsed as JSON due to err: %w", err)
		} else if errors.Is(err, &json.UnmarshalTypeError{}) {
			var e *json.UnmarshalTypeError
			if ok := errors.As(err, &e); ok {
				return 0, fmt.Errorf("the version in the state file is %s. A positive whole number is required", e.Value)
			}
			return 0, fmt.Errorf("the state file could not be parsed as JSON: %w", err)
		} else {
			return 0, fmt.Errorf("the state file could not be parsed as JSON: %w", err)
		}
	}

	if sniff.Version == nil {
		return 0, errors.New("the state file does not have a \"version\" attribute, which is required to identify the format version")
	}

	return *sniff.Version, nil
}

// sniffJSONStateTerraformVersion accepts []byte of state file and returns the version in a string format
func sniffJSONStateTerraformVersion(src []byte) string {
	type VersionSniff struct {
		Version string `json:"terraform_version"`
	}
	var sniff VersionSniff

	err := json.Unmarshal(src, &sniff)
	if err != nil {
		return ""
	}

	// Attempt to parse the string as a version so we won't report garbage
	// as a version number.
	_, err = gover.NewVersion(sniff.Version)
	if err != nil {
		return ""
	}

	return sniff.Version
}

// looksLikeVersion0 determines if the version in the []byte terraform state file is of type version 0
func looksLikeVersion0(src []byte) bool {
	// Version 0 was a custom binary format, which would not begin with '{'
	// or '[' characters.
	for _, b := range src {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '{', '[':
			return false
		default:
			// Any other non-whitespace character at the start means it's
			// probably not JSON, and so it must be a version 0 file.
			return true
		}
	}
	return false // Empty or all whitespace, so not version 0
}

func (sv StateVersionV4) MarshalJSON() ([]byte, error) {
	return []byte{'4'}, nil
}

func (sv StateVersionV4) UnmarshalJSON([]byte) error {
	// Nothing to do: we already know we're version 4
	return nil
}
