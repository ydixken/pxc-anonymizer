// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package pxc

import (
	"crypto/sha256"
	"encoding/hex"
)

// LabelValue keeps resource identities distinguishable when their names exceed the label limit.
func LabelValue(name string) string {
	if len(name) <= 63 {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	return name[:46] + "-" + hex.EncodeToString(sum[:8])
}
