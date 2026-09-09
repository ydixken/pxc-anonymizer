// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package mysqlfixtures

import "embed"

// Files keeps the seed command independent of the container working directory.
//
//go:embed schema.sql seed.sql finish.sql
var Files embed.FS
