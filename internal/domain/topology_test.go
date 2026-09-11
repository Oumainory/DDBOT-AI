package domain

import "testing"

func TestConnectorTopologyAndSatoriAmbiguity(t *testing.T) {
	main := Connector{Kind: ConnectorOneBot, Name: "OneBot", Role: ConnectorMain, Enabled: true}
	extra := Connector{Kind: ConnectorTelegram, Name: "Telegram", Role: ConnectorExtra, Enabled: true}
	if err := ValidateConnectorTopology([]Connector{main, extra}); err != nil {
		t.Fatal(err)
	}
	other := Connector{Kind: ConnectorSatori, Name: "Satori", Role: ConnectorMain, Enabled: true}
	if err := ValidateConnectorTopology([]Connector{main, other}); err == nil {
		t.Fatal("two active main connectors were accepted")
	}
	channels := []SatoriChannel{{ID: "c1", GuildID: "g1", Text: true, Sendable: true}}
	selected, err := ResolveSatoriTarget("g1", channels, "")
	if err != nil || selected.ChannelID != "c1" {
		t.Fatalf("single Satori channel = %#v, %v", selected, err)
	}
	channels = append(channels, SatoriChannel{ID: "c2", GuildID: "g1", Text: true, Sendable: true})
	if _, err := ResolveSatoriTarget("g1", channels, ""); err != ErrAmbiguousTarget {
		t.Fatalf("ambiguous Satori channels error = %v", err)
	}
	selected, err = ResolveSatoriTarget("g1", channels, "c2")
	if err != nil || selected.ChannelID != "c2" {
		t.Fatalf("explicit Satori channel = %#v, %v", selected, err)
	}
}
