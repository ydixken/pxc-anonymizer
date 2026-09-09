package sqlstep

import (
	"testing"

	"github.com/ydixken/pxc-anonymizer/internal/runner/checkpoint"
)

func TestDryRunNeedsNoConnection(t *testing.T) {
	const database = "sample"
	ctx := t.Context()
	if err := Execute(ctx, nil, database, "DROP TABLE important", true); err != nil {
		t.Fatal(err)
	}
	if err := Truncate(ctx, nil, database, "important", true); err != nil {
		t.Fatal(err)
	}
	key := checkpoint.Key{RunUID: database, Kind: "step", Database: database, Name: "pre", PolicyHash: "hash"}
	if executed, err := Run(ctx, nil, database, "DELETE FROM important", key, true); err != nil || executed {
		t.Fatalf("dry-run executed a statement: %t %v", executed, err)
	}
}
