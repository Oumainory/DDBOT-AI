package bilibili

import (
	"github.com/Oumainory/DDBOT-AI/internal/test"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestRelationModify(t *testing.T) {
	_, err := RelationModify(test.UID1, ActSub)
	assert.NotNil(t, err)
}
