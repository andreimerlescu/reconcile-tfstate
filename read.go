package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// Read reads a state from the given reader.
func Read(r io.Reader) (*TFStateFile, error) {
	if f, ok := r.(*os.File); ok && f == nil {
		return nil, ErrNoState
	}

	src, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("failed to read state file: %w", err)
	}

	if len(src) == 0 {
		return nil, ErrNoState
	}

	state, err := readState(src)
	if err != nil {
		return nil, err
	}

	if state == nil {
		panic("readState returned nil state with no errors")
	}

	return state, nil
}

// readState processes the bytes and returns a *TFStateFile resource
func readState(src []byte) (*TFStateFile, error) {
	if looksLikeVersion0(src) {
		return nil, errors.New("the state is stored in a legacy binary format that is not supported since Terraform v0.7. To continue, first upgrade the state using Terraform 0.6.16 or earlier")
	}

	version, err := sniffJSONStateVersion(src)
	if err != nil {
		return nil, err
	}

	switch version {
	case 0:
		return nil, errors.New("the state file uses JSON syntax but has a version number of zero. There was never a JSON-based state format zero, so this state file is invalid and cannot be processed")
	case 1:
		return nil, errors.New("version1 terraform state files not supported")
	case 2:
		return nil, errors.New("version2 terraform state files not supported")
	case 3:
		return nil, errors.New("version3 terraform state files not supported")
	case 4:
		return readStateV4(src)
	default:
		creatingVersion := sniffJSONStateTerraformVersion(src)
		if creatingVersion != "" {
			return nil, fmt.Errorf("the state file uses format version %d, which is not supported by this program. This state file was created by Terraform %s", version, creatingVersion)
		}
		return nil, fmt.Errorf("the state file uses format version %d, which is not supported by this program. This state file may have been created by a newer version of Terraform", version)
	}
}

// readStateV4 uses json.Unmarshal from the []byte to return a *TFStateFile resource
func readStateV4(src []byte) (*TFStateFile, error) {
	var s StateFileV4
	err := json.Unmarshal(src, &s)
	if err != nil {
		return nil, fmt.Errorf("failed to parse state file as version 4: %w", err)
	}

	// Convert StateFileV4 to the common TFStateFile struct
	f := &TFStateFile{
		Version:          4,
		TerraformVersion: s.TerraformVersion,
		Serial:           s.Serial,
		Lineage:          s.Lineage,
		RootOutputs:      make(map[string]OutputStateV4),
		Resources:        make([]ResourceStateV4, len(s.Resources)),
		CheckResults:     s.CheckResults,
	}

	for k, v := range s.RootOutputs {
		f.RootOutputs[k] = v
	}
	for i, r := range s.Resources {
		f.Resources[i] = r
	}

	return f, nil
}
