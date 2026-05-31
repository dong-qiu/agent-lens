package ingest

import (
	"strings"
	"testing"

	"github.com/dong-qiu/agent-lens/internal/pb"
)

// TestValidKindsMatchProtoEnum guards the wire-kind allowlist against drift from
// the canonical proto EventKind enum: every enum value (except UNSPECIFIED) must
// have a lowercase entry in validKinds, and vice versa. Adding a kind to one
// list but not the other silently drops it on the wire (rejected by ingest) or
// accepts a kind the schema doesn't know — exactly the kind of split-brain a
// hand-maintained allowlist invites.
func TestValidKindsMatchProtoEnum(t *testing.T) {
	fromProto := map[string]bool{}
	for n, name := range pb.EventKind_name {
		if n == 0 { // EVENT_KIND_UNSPECIFIED is not a wire value
			continue
		}
		wire := strings.ToLower(strings.TrimPrefix(name, "EVENT_KIND_"))
		fromProto[wire] = true
		if _, ok := validKinds[wire]; !ok {
			t.Errorf("proto EventKind %q (%s) missing from validKinds", wire, name)
		}
	}
	for k := range validKinds {
		if !fromProto[k] {
			t.Errorf("validKinds has %q with no matching proto EventKind", k)
		}
	}
}
