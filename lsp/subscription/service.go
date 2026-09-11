// Package subscription is the single mutation/read boundary shared by the
// existing chat commands and the Phase 3 Dashboard.  It delegates to the
// already-registered Concern implementations, so BuntDB remains the only
// Legacy subscription authority.
package subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cnxysoft/DDBOT-WSa/adapter"
	"github.com/cnxysoft/DDBOT-WSa/internal/domain"
	"github.com/cnxysoft/DDBOT-WSa/lsp/concern"
	"github.com/cnxysoft/DDBOT-WSa/lsp/concern_type"
	"github.com/cnxysoft/DDBOT-WSa/lsp/mmsg"
	"github.com/sirupsen/logrus"
	"github.com/tidwall/buntdb"
)

type Service struct{}

func NewService() *Service { return &Service{} }

type Request struct {
	Site      string
	ID        string
	Type      string
	GroupCode int64
}

type OptionsPatch struct {
	Text        *[]string `json:"text,omitempty"`
	NotText     *[]string `json:"not_text,omitempty"`
	Types       *[]string `json:"type,omitempty"`
	NotTypes    *[]string `json:"not_type,omitempty"`
	TitleChange *string   `json:"title_change_notify,omitempty"`
	Offline     *string   `json:"offline_notify,omitempty"`
	Enhanced    *string   `json:"extend_notify,omitempty"`
}

type noopContext struct{ groupCode int64 }

func (c noopContext) TextSend(string) interface{}    { return nil }
func (c noopContext) TextReply(string) interface{}   { return nil }
func (c noopContext) Reply(*mmsg.MSG) interface{}    { return nil }
func (c noopContext) Send(*mmsg.MSG) interface{}     { return nil }
func (c noopContext) NoPermissionReply() interface{} { return nil }
func (c noopContext) GetLog() *logrus.Entry          { return logrus.NewEntry(logrus.StandardLogger()) }
func (c noopContext) GetTarget() mmsg.Target         { return mmsg.NewGroupTarget(c.groupCode) }
func (c noopContext) GetSender() *adapter.SenderInfo { return &adapter.SenderInfo{UserID: 0, Uin: 0} }

func (s *Service) resolve(request Request) (concern.Concern, interface{}, concern_type.Type, error) {
	site := strings.TrimSpace(request.Site)
	ctypeRaw := strings.TrimSpace(request.Type)
	cm, site, ctype, err := concern.GetConcernByParseSiteAndType(site, ctypeRaw)
	if err != nil {
		return nil, nil, "", err
	}
	id, err := cm.ParseId(strings.TrimSpace(request.ID))
	if err != nil {
		return nil, nil, "", err
	}
	return cm, id, ctype, nil
}

func (s *Service) Subscribe(ctx context.Context, request Request) (concern.IdentityInfo, error) {
	return s.SubscribeWithMessageContext(noopContext{groupCode: request.GroupCode}, request)
}

func (s *Service) SubscribeWithMessageContext(msgCtx mmsg.IMsgCtx, request Request) (concern.IdentityInfo, error) {
	cm, id, ctype, err := s.resolve(request)
	if err != nil {
		return nil, err
	}
	if msgCtx == nil {
		msgCtx = noopContext{groupCode: request.GroupCode}
	}
	return cm.Add(msgCtx, request.GroupCode, id, ctype)
}

func (s *Service) Unsubscribe(ctx context.Context, request Request) (concern.IdentityInfo, error) {
	return s.UnsubscribeWithMessageContext(noopContext{groupCode: request.GroupCode}, request)
}

func (s *Service) UnsubscribeWithMessageContext(msgCtx mmsg.IMsgCtx, request Request) (concern.IdentityInfo, error) {
	cm, id, ctype, err := s.resolve(request)
	if err != nil {
		return nil, err
	}
	if msgCtx == nil {
		msgCtx = noopContext{groupCode: request.GroupCode}
	}
	return cm.Remove(msgCtx, request.GroupCode, id, ctype)
}

// UpdateOptions changes only the existing structured Legacy configuration. It
// does not expose a YAML/editor escape hatch and uses the same StateManager
// transaction used by chat commands.
func (s *Service) UpdateOptions(ctx context.Context, request Request, patch OptionsPatch) error {
	cm, id, _, err := s.resolve(request)
	if err != nil {
		return err
	}
	sm := cm.GetStateManager()
	cfg := sm.GetGroupConcernConfig(request.GroupCode, id)
	return sm.OperateGroupConcernConfig(request.GroupCode, id, cfg, func(value concern.IConfig) bool {
		filter := value.GetGroupConcernFilter()
		if patch.Text != nil {
			bytes, _ := json.Marshal(map[string]any{"text": *patch.Text})
			filter.SetRule(concern.FilterTypeText, string(bytes))
		}
		if patch.NotText != nil {
			bytes, _ := json.Marshal(map[string]any{"text": *patch.NotText})
			filter.SetRule(concern.FilterTypeNotText, string(bytes))
		}
		if patch.Types != nil {
			bytes, _ := json.Marshal(map[string]any{"type": *patch.Types})
			filter.SetRule(concern.FilterTypeType, string(bytes))
		}
		if patch.NotTypes != nil {
			bytes, _ := json.Marshal(map[string]any{"type": *patch.NotTypes})
			filter.SetRule(concern.FilterTypeNotType, string(bytes))
		}
		if patch.TitleChange != nil {
			value.GetGroupConcernNotify().TitleChangeNotify = concern_type.FromString(strings.TrimSpace(*patch.TitleChange))
		}
		if patch.Offline != nil {
			value.GetGroupConcernNotify().OfflineNotify = concern_type.FromString(strings.TrimSpace(*patch.Offline))
		}
		if patch.Enhanced != nil {
			value.GetGroupConcernNotify().ExtendNotify = concern_type.FromString(strings.TrimSpace(*patch.Enhanced))
		}
		return true
	})
}

// Snapshot reads all current BuntDB relationships and returns a rebuildable
// projection input. It has no write path and is safe to call asynchronously.
func (s *Service) Snapshot(ctx context.Context) ([]domain.LegacySubscription, error) {
	var result []domain.LegacySubscription
	for _, cm := range concern.ListConcern() {
		groups, ids, types, err := cm.GetStateManager().ListConcernState(func(int64, interface{}, concern_type.Type) bool { return true })
		if err != nil {
			return nil, err
		}
		for index := range ids {
			if index >= len(groups) || index >= len(types) {
				continue
			}
			identity, _ := cm.Get(ids[index])
			externalID := fmt.Sprint(ids[index])
			name := "unknown"
			if identity != nil && identity.GetName() != "" {
				name = identity.GetName()
			}
			options := map[string]string{"type": types[index].String()}
			if cfg := cm.GetStateManager().GetGroupConcernConfig(groups[index], ids[index]); cfg != nil {
				if raw, marshalErr := json.Marshal(cfg); marshalErr == nil {
					var value any
					if json.Unmarshal(raw, &value) == nil {
						if canonical, marshalErr := json.Marshal(value); marshalErr == nil {
							options = map[string]string{"config": string(canonical)}
						}
					}
				}
			}
			result = append(result, domain.LegacySubscription{
				Platform: cm.Site(), ExternalID: externalID, DisplayName: name,
				SubscriptionType: types[index].String(), TargetType: string(domain.TargetGroup),
				TargetExternalID: fmt.Sprintf("%d", groups[index]), TargetDisplayName: fmt.Sprintf("QQ Group %d", groups[index]),
				Enabled: true, LegacyKey: fmt.Sprintf("%s:%s:%s|group:%d", cm.Site(), externalID, types[index], groups[index]),
				OptionsJSON: encodeOptions(options),
			})
		}
	}
	return result, nil
}

func encodeOptions(options map[string]string) string {
	if raw, err := json.Marshal(options); err == nil {
		return string(raw)
	}
	return "{}"
}

// IsNotFound is shared by the HTTP mapping and tests without making BuntDB a
// second domain authority.
func IsNotFound(err error) bool { return err == buntdb.ErrNotFound }
