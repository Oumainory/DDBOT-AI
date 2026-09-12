package concern

import (
	"fmt"

	"github.com/Oumainory/DDBOT-AI/internal/observation"
)

func observationInput(event Event) (input observation.EventInput) {
	defer func() {
		if recover() != nil {
			input = observation.EventInput{}
		}
	}()
	input = observation.EventInput{
		Platform:         event.Site(),
		SourceKind:       event.Site(),
		SourceExternalID: fmt.Sprint(event.GetUid()),
		EventType:        event.Type().String(),
	}
	if event.GetUid() == nil {
		input.SourceExternalID = ""
	}
	if provider, ok := event.(observation.PublicSnapshotProvider); ok {
		input.PublicText = provider.ObservationPublicText()
		input.PublicURL = provider.ObservationPublicURL()
		input.PublicMediaURLs = provider.ObservationPublicMediaURLs()
		input.PublicAuthorID = provider.ObservationPublicAuthorID()
		input.PublicAuthorName = provider.ObservationPublicAuthorName()
		input.UpstreamEventID = provider.ObservationUpstreamEventID()
		input.SourceEventAt = provider.ObservationSourceEventAt()
	}
	return input
}
