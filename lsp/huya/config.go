package huya

import (
	"github.com/Oumainory/DDBOT-AI/lsp/concern"
)

type GroupConcernConfig struct {
	concern.IConfig
}

func NewGroupConcernConfig(g concern.IConfig) *GroupConcernConfig {
	return &GroupConcernConfig{g}
}
