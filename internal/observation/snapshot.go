package observation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// publicSnapshot is intentionally a fixed struct. Adding a source field
// requires an explicit review rather than accidentally persisting a raw API
// response with json.Marshal.
type publicSnapshot struct {
	Platform         string   `json:"platform"`
	SourceKind       string   `json:"source_kind"`
	SourceExternalID string   `json:"source_external_id,omitempty"`
	UpstreamEventID  string   `json:"upstream_event_id,omitempty"`
	EventType        string   `json:"event_type"`
	SourceEventAt    *int64   `json:"source_event_at,omitempty"`
	Text             string   `json:"text,omitempty"`
	URL              string   `json:"url,omitempty"`
	MediaURLs        []string `json:"media_urls,omitempty"`
	AuthorID         string   `json:"author_id,omitempty"`
	AuthorName       string   `json:"author_name,omitempty"`
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	cloned := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			cloned = append(cloned, trimmed)
		}
	}
	if len(cloned) == 0 {
		return nil
	}
	return cloned
}

func makePublicSnapshot(input EventInput) (string, string) {
	var sourceEventAt *int64
	if !input.SourceEventAt.IsZero() {
		stamp := input.SourceEventAt.UTC().Unix()
		sourceEventAt = &stamp
	}
	snapshot := publicSnapshot{
		Platform:         strings.TrimSpace(input.Platform),
		SourceKind:       strings.TrimSpace(input.SourceKind),
		SourceExternalID: strings.TrimSpace(input.SourceExternalID),
		UpstreamEventID:  strings.TrimSpace(input.UpstreamEventID),
		EventType:        strings.TrimSpace(input.EventType),
		SourceEventAt:    sourceEventAt,
		Text:             input.PublicText,
		URL:              input.PublicURL,
		MediaURLs:        cloneStrings(input.PublicMediaURLs),
		AuthorID:         input.PublicAuthorID,
		AuthorName:       input.PublicAuthorName,
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return "", ""
	}
	digest := sha256.Sum256(encoded)
	return string(encoded), hex.EncodeToString(digest[:])
}
