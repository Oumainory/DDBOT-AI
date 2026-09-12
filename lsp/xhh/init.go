package xhh

import (
	"github.com/Oumainory/DDBOT-AI/lsp/concern"
)

func init() {
	concern.RegisterConcern(NewConcern(concern.GetNotifyChan()))
}
