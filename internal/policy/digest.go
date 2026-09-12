package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"

	"github.com/Oumainory/DDBOT-AI/internal/domain"
)

// Digest returns a deterministic digest of the policy semantics that can
// change a DROP decision. Provenance labels, UI text and operational knobs
// are deliberately excluded so an approval is not invalidated by cosmetic or
// transport-only changes.
func Digest(value EffectivePolicy) string {
	categories := make(map[string]string, len(value.CategoryActions))
	for key, action := range value.CategoryActions {
		categories[string(key)] = string(action)
	}
	tags := make(map[string]string, len(value.TagActions))
	for key, action := range value.TagActions {
		tags[key] = string(action)
	}
	canonical := struct {
		Mode       string            `json:"mode"`
		Threshold  string            `json:"threshold"`
		Categories map[string]string `json:"categories"`
		Tags       map[string]string `json:"tags"`
		Safety     string            `json:"safety"`
	}{string(value.Mode), strconv.FormatFloat(value.Threshold, 'f', -1, 64), categories, tags, "hard-pass-v1"}
	raw, _ := json.Marshal(canonical)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ProfileDigest is the corresponding digest for a single profile. Map keys
// are sorted before encoding to make the contract independent of map
// iteration order and Go version.
func ProfileDigest(value Profile) string {
	categories := make(map[string]string, len(value.CategoryActions))
	for key, action := range value.CategoryActions {
		categories[string(key)] = string(action)
	}
	tags := make(map[string]string, len(value.TagActions))
	for key, action := range value.TagActions {
		tags[key] = string(action)
	}
	safety := make(map[string]bool, len(value.Safety))
	for key, enabled := range value.Safety {
		safety[key] = enabled
	}
	canonical := struct {
		Default    string            `json:"default"`
		Categories map[string]string `json:"categories"`
		Tags       map[string]string `json:"tags"`
		Safety     map[string]bool   `json:"safety"`
	}{string(value.DefaultAction), categories, tags, safety}
	raw, _ := json.Marshal(canonical)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// CanonicalCategories is useful to callers that need a stable display or
// audit representation without exposing map iteration order.
func CanonicalCategories(values map[domain.Category]Action) []string {
	result := make([]string, 0, len(values))
	for key, action := range values {
		result = append(result, string(key)+"="+string(action))
	}
	sort.Strings(result)
	return result
}
