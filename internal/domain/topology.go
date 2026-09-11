package domain

import "strings"

// ValidateConnectorTopology enforces the V1 topology: at most one enabled
// main connector, and Telegram is allowed only as an extra publisher.
func ValidateConnectorTopology(connectors []Connector) error {
	mainCount := 0
	for _, connector := range connectors {
		if !connector.Enabled {
			continue
		}
		if err := ValidateConnector(connector); err != nil {
			return err
		}
		if connector.Role == ConnectorMain {
			mainCount++
			if connector.Kind != ConnectorOneBot && connector.Kind != ConnectorSatori {
				return ErrTopologyInvalid
			}
		}
		if connector.Kind == ConnectorTelegram && connector.Role != ConnectorExtra {
			return ErrTopologyInvalid
		}
	}
	if mainCount > 1 {
		return ErrTopologyInvalid
	}
	return nil
}

type SatoriChannel struct {
	ID       string
	Name     string
	GuildID  string
	Text     bool
	Sendable bool
}

type SatoriTargetSelection struct {
	GuildID   string
	ChannelID string
}

// ResolveSatoriTarget never guesses between multiple possible text channels.
func ResolveSatoriTarget(guildID string, channels []SatoriChannel, explicitChannelID string) (SatoriTargetSelection, error) {
	guildID = strings.TrimSpace(guildID)
	explicitChannelID = strings.TrimSpace(explicitChannelID)
	if guildID == "" {
		return SatoriTargetSelection{}, ErrInvalidDomain
	}
	if explicitChannelID != "" {
		for _, channel := range channels {
			if channel.ID == explicitChannelID && channel.GuildID == guildID && channel.Text && channel.Sendable {
				return SatoriTargetSelection{GuildID: guildID, ChannelID: channel.ID}, nil
			}
		}
		return SatoriTargetSelection{}, ErrAmbiguousTarget
	}
	var selected SatoriChannel
	count := 0
	for _, channel := range channels {
		if channel.GuildID == guildID && channel.Text && channel.Sendable {
			selected = channel
			count++
		}
	}
	if count != 1 {
		return SatoriTargetSelection{}, ErrAmbiguousTarget
	}
	return SatoriTargetSelection{GuildID: guildID, ChannelID: selected.ID}, nil
}
