package language

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecoverParseReturnsConciseError(t *testing.T) {
	t.Parallel()

	cfg, err := recoverParse(func() (*MappingConfig, error) {
		panic("boom")
	})

	require.Nil(t, cfg)
	require.Error(t, err)

	var verr *ValidationError
	require.ErrorAs(t, err, &verr)
	assert.Contains(t, verr.Message, "panic during parsing")
	assert.Contains(t, verr.Message, "boom")
	// The message must not leak a stack trace: no source file paths, no newlines.
	assert.NotContains(t, verr.Message, ".go:")
	assert.NotContains(t, verr.Message, "\n")
}
