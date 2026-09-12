package telegram

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/Oumainory/DDBOT-AI/internal/domain"
	"github.com/Oumainory/DDBOT-AI/internal/pairing"
	"github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// PairingVerifier performs the live Telegram-side checks required before a
// target can become active. It intentionally returns only canonical,
// connector-neutral identity data; the bot token and raw API responses never
// leave this adapter package.
type PairingVerifier struct{}

func (PairingVerifier) VerifyTarget(ctx context.Context, _ string, value pairing.Verification) (pairing.Verification, error) {
	if ctx != nil {
		select {
		case <-ctx.Done():
			return pairing.Verification{}, ctx.Err()
		default:
		}
	}
	if value.TargetType != domain.TargetGroup && value.TargetType != domain.TargetChannel {
		return pairing.Verification{}, errors.New("telegram verification: unsupported target type")
	}
	identity := strings.TrimSpace(value.ExternalID)
	if identity == "" || !ensureInit() || bot == nil || bot.Self.ID == 0 {
		return pairing.Verification{}, errors.New("telegram verification unavailable")
	}
	chatConfig := tgbotapi.ChatInfoConfig{}
	chatID, parseErr := strconv.ParseInt(identity, 10, 64)
	if parseErr == nil {
		chatConfig.ChatID = chatID
	} else if strings.HasPrefix(identity, "@") && len(identity) > 1 {
		chatConfig.SuperGroupUsername = identity
	} else {
		return pairing.Verification{}, errors.New("telegram verification: target identity is ambiguous")
	}
	chat, err := bot.GetChat(chatConfig)
	if err != nil {
		return pairing.Verification{}, errors.New("telegram verification: target unavailable")
	}
	canonicalType := domain.TargetGroup
	switch chat.Type {
	case "group", "supergroup":
		if value.TargetType != domain.TargetGroup {
			return pairing.Verification{}, errors.New("telegram verification: target type mismatch")
		}
	case "channel":
		canonicalType = domain.TargetChannel
		if value.TargetType != domain.TargetChannel {
			return pairing.Verification{}, errors.New("telegram verification: target type mismatch")
		}
	default:
		return pairing.Verification{}, errors.New("telegram verification: unsupported chat")
	}
	member, err := bot.GetChatMember(tgbotapi.GetChatMemberConfig{ChatConfigWithUser: tgbotapi.ChatConfigWithUser{ChatID: chat.ID, UserID: bot.Self.ID}})
	if err != nil || !telegramBotCanAddress(member, canonicalType) {
		return pairing.Verification{}, errors.New("telegram verification: bot cannot send to target")
	}
	name := strings.TrimSpace(value.DisplayName)
	if name == "" {
		name = strings.TrimSpace(chat.Title)
	}
	if name == "" {
		name = strings.TrimSpace(chat.UserName)
	}
	value.TargetType = canonicalType
	value.ExternalID = strconv.FormatInt(chat.ID, 10)
	value.DisplayName = name
	value.Metadata = map[string]any{"telegram_chat_type": chat.Type}
	value.Verified = true
	return value, nil
}

func telegramBotCanAddress(member tgbotapi.ChatMember, targetType domain.TargetType) bool {
	switch member.Status {
	case "creator":
		return true
	case "administrator":
		if targetType == domain.TargetChannel {
			return member.CanPostMessages
		}
		return true
	case "member":
		return targetType == domain.TargetGroup
	case "restricted":
		return targetType == domain.TargetGroup && member.CanSendMessages
	default:
		return false
	}
}
