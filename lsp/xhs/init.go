package xhs

import (
	"github.com/Oumainory/DDBOT-AI/lsp/concern"
)

func init() {
	c := NewConcern(concern.GetNotifyChan())
	concern.RegisterConcern(c)
}
