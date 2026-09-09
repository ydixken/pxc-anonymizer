// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
)

var component = regexp.MustCompile(`^[A-Za-z0-9._-]{1,253}$`)

// Kubernetes projections use symlinks; validate components without rejecting legitimate projections.
func projectedPath(root string, parts ...string) (string, error) {
	if root == "" {
		return "", errors.New("projected directory is required")
	}
	for _, part := range parts {
		if part == "." || part == ".." || !component.MatchString(part) {
			return "", errors.New("invalid projected path component")
		}
	}
	return filepath.Join(append([]string{root}, parts...)...), nil
}

func ReadFile(path string, maximum int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("required input file cannot be opened")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("required input is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(data)) > maximum {
		return nil, errors.New("required input file cannot be read within its size limit")
	}
	return data, nil
}

func constantValue(rule api.ColumnRule, root string) (*string, error) {
	if rule.Params == nil || rule.Params.ValueFrom == nil {
		return nil, nil
	}
	ref := rule.Params.ValueFrom
	path, err := projectedPath(root, ref.Name, ref.Key)
	if err != nil {
		return nil, err
	}
	data, err := ReadFile(path, 1<<20)
	if err != nil {
		return nil, err
	}
	return new(string(data)), nil
}
