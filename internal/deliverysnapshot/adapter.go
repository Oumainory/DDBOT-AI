package deliverysnapshot

// This file contains the one-way boundary from the Legacy adapter message
// model into the connector-neutral snapshot model. It intentionally returns
// plain JSON values: no adapter element, renderer, file handle, or callback is
// retained in a payload that may outlive the process which created it.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/cnxysoft/DDBOT-WSa/adapter"
)

var (
	ErrUnsupportedElement = errors.New("deliverysnapshot: unsupported message element")
	ErrSensitiveValue     = errors.New("deliverysnapshot: sensitive value is not durable")
	ErrInvalidRemoteMedia = errors.New("deliverysnapshot: invalid remote media URL")
)

// FromSendingMessage converts the already-packed adapter message into the
// stable v1 segment schema. It is deliberately called after Legacy filtering
// and before the Messenger boundary, so a successful return represents the
// exact ordered message that would otherwise have been sent.
func FromSendingMessage(message *adapter.SendingMessage) (MessageSnapshot, error) {
	if message == nil || len(message.Elements) == 0 {
		return MessageSnapshot{}, ErrMissingMessage
	}
	segments := make([]Segment, 0, len(message.Elements))
	media := make([]MediaReference, 0)
	var fallback strings.Builder
	for _, element := range message.Elements {
		segment, refs, err := segmentFromElement(element)
		if err != nil {
			return MessageSnapshot{}, err
		}
		segments = append(segments, segment)
		media = append(media, refs...)
		if text, ok := segment.Data["content"].(string); ok && segment.Type == "text" {
			fallback.WriteString(text)
		}
	}
	digest, err := digestJSON(segments)
	if err != nil {
		return MessageSnapshot{}, err
	}
	return MessageSnapshot{
		SchemaVersion: CurrentSchemaVersion,
		Segments:      segments,
		Media:         media,
		TextFallback:  fallback.String(),
		TemplateName:  "legacy",
		TemplateHash:  digest,
	}, nil
}

// FromForwardMessage captures a merge-forward request, including its ordered
// node list and top-level options, without retaining the original maps. The
// caller must still choose a connector-specific sender at release time.
func FromForwardMessage(nodes []map[string]interface{}, options *adapter.ForwardOptions) (MessageSnapshot, error) {
	if len(nodes) == 0 {
		return MessageSnapshot{}, ErrMissingMessage
	}
	cloned := make([]map[string]any, 0, len(nodes))
	for _, node := range nodes {
		value, err := cloneJSONValue(node)
		if err != nil {
			return MessageSnapshot{}, err
		}
		object, ok := value.(map[string]any)
		if !ok {
			return MessageSnapshot{}, ErrUnsupportedElement
		}
		if err := rejectSensitiveJSON(object); err != nil {
			return MessageSnapshot{}, err
		}
		cloned = append(cloned, object)
	}
	data := map[string]any{"nodes": cloned}
	if options != nil {
		data["options"] = map[string]any{
			"prompt": options.Prompt, "source": options.Source,
			"summary": options.Summary, "news": append([]string(nil), options.News...),
		}
	}
	segments := []Segment{{Type: "forward", Data: data}}
	digest, err := digestJSON(segments)
	if err != nil {
		return MessageSnapshot{}, err
	}
	return MessageSnapshot{SchemaVersion: CurrentSchemaVersion, Segments: segments, TemplateName: "legacy", TemplateHash: digest}, nil
}

func segmentFromElement(element adapter.IMessageElement) (Segment, []MediaReference, error) {
	if element == nil {
		return Segment{}, nil, ErrUnsupportedElement
	}
	switch value := element.(type) {
	case *adapter.TextSegment:
		if value == nil {
			return Segment{}, nil, ErrUnsupportedElement
		}
		return Segment{Type: "text", Data: map[string]any{"content": value.Content}}, nil, nil
	case *adapter.ImageSegment:
		if value == nil {
			return Segment{}, nil, ErrUnsupportedElement
		}
		data := map[string]any{"file": value.File, "url": value.Url}
		refs := make([]MediaReference, 0, 2)
		for _, candidate := range []string{value.File, value.Url} {
			if strings.TrimSpace(candidate) == "" {
				continue
			}
			ref, err := durableMedia(candidate)
			if err != nil {
				return Segment{}, nil, err
			}
			refs = append(refs, ref)
		}
		return Segment{Type: "image", Data: data}, refs, nil
	case *adapter.FaceSegment:
		if value == nil {
			return Segment{}, nil, ErrUnsupportedElement
		}
		return Segment{Type: "face", Data: map[string]any{"index": value.Index, "name": value.Name}}, nil, nil
	case *adapter.AtSegment:
		if value == nil {
			return Segment{}, nil, ErrUnsupportedElement
		}
		return Segment{Type: "mention", Data: map[string]any{"target": value.Target, "display": value.Display}}, nil, nil
	case *adapter.ReplySegment:
		if value == nil {
			return Segment{}, nil, ErrUnsupportedElement
		}
		return Segment{Type: "reply", Data: map[string]any{"reply_seq": value.ReplySeq, "id": value.Id, "sender": value.Sender, "group_id": value.GroupID, "time": value.Time}}, nil, nil
	case *adapter.JsonSegment:
		if value == nil {
			return Segment{}, nil, ErrUnsupportedElement
		}
		return Segment{Type: "service", Data: map[string]any{"content": value.Content}}, nil, nil
	case *adapter.ForwardSegment:
		if value == nil {
			return Segment{}, nil, ErrUnsupportedElement
		}
		return Segment{Type: "forward", Data: map[string]any{"res_id": value.ResId}}, nil, nil
	case *adapter.FileSegment:
		if value == nil {
			return Segment{}, nil, ErrUnsupportedElement
		}
		data := map[string]any{"name": value.Name, "path": value.Path, "id": value.Id, "url": value.Url, "busid": value.Busid, "size": value.Size}
		refs := make([]MediaReference, 0, 2)
		for _, candidate := range []string{value.Path, value.Url} {
			if strings.TrimSpace(candidate) == "" {
				continue
			}
			ref, err := durableMedia(candidate)
			if err != nil {
				return Segment{}, nil, err
			}
			refs = append(refs, ref)
		}
		return Segment{Type: "file", Data: data}, refs, nil
	case *adapter.VoiceSegment:
		if value == nil {
			return Segment{}, nil, ErrUnsupportedElement
		}
		data := map[string]any{"name": value.Name, "md5": hex.EncodeToString(value.Md5), "size": value.Size, "url": value.Url}
		if len(value.Data) > 0 {
			data["data_base64"] = base64.StdEncoding.EncodeToString(value.Data)
		}
		refs := make([]MediaReference, 0, 1)
		if strings.TrimSpace(value.Url) != "" {
			ref, err := durableMedia(value.Url)
			if err != nil {
				return Segment{}, nil, err
			}
			refs = append(refs, ref)
		}
		return Segment{Type: "voice", Data: data}, refs, nil
	case *adapter.VideoSegment:
		if value == nil {
			return Segment{}, nil, ErrUnsupportedElement
		}
		data := map[string]any{"name": value.Name, "uuid": hex.EncodeToString(value.Uuid), "size": value.Size, "thumb_size": value.ThumbSize, "md5": hex.EncodeToString(value.Md5), "thumb_md5": hex.EncodeToString(value.ThumbMd5), "url": value.Url}
		refs := make([]MediaReference, 0, 1)
		if strings.TrimSpace(value.Url) != "" {
			ref, err := durableMedia(value.Url)
			if err != nil {
				return Segment{}, nil, err
			}
			refs = append(refs, ref)
		}
		return Segment{Type: "video", Data: data}, refs, nil
	default:
		return Segment{}, nil, fmt.Errorf("%w: %T", ErrUnsupportedElement, element)
	}
}

func durableMedia(raw string) (MediaReference, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return MediaReference{}, ErrInvalidRemoteMedia
	}
	if strings.HasPrefix(raw, "base64://") {
		sum := sha256.Sum256([]byte(raw[len("base64://"):]))
		return MediaReference{Fallback: raw, SHA256: "sha256:" + hex.EncodeToString(sum[:]), Durable: true}, nil
	}
	if strings.HasPrefix(strings.ToLower(raw), "file://") {
		return MediaReference{}, ErrNonDurableMedia
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return MediaReference{}, ErrInvalidRemoteMedia
	}
	for key := range parsed.Query() {
		if sensitiveMediaKey(key) {
			return MediaReference{}, ErrSensitiveValue
		}
	}
	sum := sha256.Sum256([]byte(raw))
	return MediaReference{RemoteURL: raw, SHA256: "sha256:" + hex.EncodeToString(sum[:]), Durable: true}, nil
}

func sensitiveMediaKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"), " ", "_"))
	for _, token := range []string{"token", "secret", "password", "credential", "api_key", "apikey", "private_key", "access_key", "authorization"} {
		if strings.Contains(key, token) {
			return true
		}
	}
	return false
}

func digestJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func cloneJSONValue(value any) (any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var cloned any
	if err := json.Unmarshal(data, &cloned); err != nil {
		return nil, err
	}
	return cloned, nil
}

func rejectSensitiveJSON(value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if sensitiveMediaKey(key) {
				return ErrSensitiveValue
			}
			if err := rejectSensitiveJSON(item); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range typed {
			if err := rejectSensitiveJSON(item); err != nil {
				return err
			}
		}
	}
	return nil
}
