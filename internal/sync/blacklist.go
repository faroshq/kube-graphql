package sync

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// DefaultBlacklist is the default set of resources to skip syncing.
// These are either too sensitive (Secrets) or too high-volume (Events).
var DefaultBlacklist = []schema.GroupVersionResource{
	{Group: "", Version: "v1", Resource: "secrets"},
	{Group: "", Version: "v1", Resource: "events"},
	{Group: "events.k8s.io", Version: "v1", Resource: "events"},
}

// Blacklist determines which resources should be excluded from syncing.
type Blacklist struct {
	entries map[schema.GroupResource]bool
}

// NewBlacklist creates a Blacklist from a list of GVRs.
func NewBlacklist(gvrs []schema.GroupVersionResource) *Blacklist {
	b := &Blacklist{entries: make(map[schema.GroupResource]bool)}
	for _, gvr := range gvrs {
		b.entries[gvr.GroupResource()] = true
	}
	return b
}

// IsBlacklisted returns true if the given group-resource should be skipped.
func (b *Blacklist) IsBlacklisted(gr schema.GroupResource) bool {
	return b.entries[gr]
}
