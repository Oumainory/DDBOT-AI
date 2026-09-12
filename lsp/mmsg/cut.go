package mmsg

import "github.com/Oumainory/DDBOT-AI/adapter"

type CutElement struct {
}

func (c *CutElement) Type() adapter.ElementType {
	return Cut
}

func (c *CutElement) PackToElement(Target) adapter.IMessageElement {
	return nil
}

func (c *CutElement) ToSendingMessage() *adapter.SendingMessage {
	return &adapter.SendingMessage{Elements: []adapter.IMessageElement{c}}
}
