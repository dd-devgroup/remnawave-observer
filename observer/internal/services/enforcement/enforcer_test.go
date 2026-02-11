package enforcement

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNoopEnforcer_DisableTempByInternalID(t *testing.T) {
	e := NewNoopEnforcer()
	ctx := context.Background()

	err := e.DisableTempByInternalID(ctx, 12345, 10*time.Minute, "test", 80)
	assert.NoError(t, err, "noop enforcer should always succeed")
}

func TestNoopEnforcer_Ping(t *testing.T) {
	e := NewNoopEnforcer()
	ctx := context.Background()

	err := e.Ping(ctx)
	assert.NoError(t, err, "noop enforcer ping should always succeed")
}
