package admin

import (
	"context"
	"testing"

	"github.com/Oumainory/DDBOT-AI/adapter"
	"github.com/Oumainory/DDBOT-AI/internal/observation"
	"github.com/Oumainory/DDBOT-AI/lsp/mmsg"
)

func TestMigrationHoldNoMigrationDoesNotSerializeUnsupportedMessage(t *testing.T) {
	holder := legacyMigrationHolder{}
	message := &adapter.SendingMessage{Elements: []adapter.IMessageElement{nil}}
	held, err := holder.holdMessage(context.Background(), message, mmsg.NewGroupTarget(1001), observation.RouteTrace{})
	if err != nil || held {
		t.Fatalf("unsupported no-migration message = held=%v err=%v, want pure no-op", held, err)
	}
}

func TestMigrationHoldNoMigrationIgnoresNonDurableMedia(t *testing.T) {
	holder := legacyMigrationHolder{}
	message := &adapter.SendingMessage{Elements: []adapter.IMessageElement{&adapter.ImageSegment{File: "file:///private/local.png"}}}
	held, err := holder.holdMessage(context.Background(), message, mmsg.NewGroupTarget(1001), observation.RouteTrace{})
	if err != nil || held {
		t.Fatalf("non-durable media no-migration message = held=%v err=%v, want pure no-op", held, err)
	}
}

func TestMigrationForwardHoldNoMigrationIgnoresUnsupportedNode(t *testing.T) {
	holder := legacyMigrationHolder{}
	held, err := holder.holdForward(context.Background(), 1001, []map[string]interface{}{nil}, nil, observation.RouteTrace{})
	if err != nil || held {
		t.Fatalf("unsupported no-migration forward = held=%v err=%v, want pure no-op", held, err)
	}
}
