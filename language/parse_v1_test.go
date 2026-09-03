package language

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParsingAcceptsVariablesField(t *testing.T) {
	t.Parallel()
	yamlStr := `
version: "1"
rules:
  - name: "test"
    variables:
      user_email: input.data.email
      org_id: input.data.org_id
    tuples:
      - user: "user:{{ .variables.user_email }}"
        relation: "member"
        object: "org:{{ .variables.org_id }}"
`
	config, err := parseMapping([]byte(yamlStr))
	require.NoError(t, err)
	require.NoError(t, config.Validate())
	require.Len(t, config.Rules[0].Variables, 2)
}
