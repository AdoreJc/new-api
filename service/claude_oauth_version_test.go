package service

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// claude-opus-5-5 rejects Claude Code clients older than 2.1.280 with a 400.
func TestClaudeCodeDefaultClientVersionMeetsModelGate(t *testing.T) {
	builtin, ok := parseClaudeCodeReleaseVersion(claudeCodeDefaultClientVersion)
	require.True(t, ok, "built-in version %q must be a plain x.y.z release", claudeCodeDefaultClientVersion)
	assert.GreaterOrEqual(t, slices.Compare(builtin[:], []int{2, 1, 280}), 0)
}

func TestResolveClaudeCodeClientVersion(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want string
	}{
		{name: "unset uses built-in", env: "", want: claudeCodeDefaultClientVersion},
		{name: "blank uses built-in", env: "  ", want: claudeCodeDefaultClientVersion},
		{name: "built-in itself", env: claudeCodeDefaultClientVersion, want: claudeCodeDefaultClientVersion},
		{name: "newer patch", env: "2.1.400", want: "2.1.400"},
		{name: "newer minor trimmed", env: " 2.2.0 ", want: "2.2.0"},
		{name: "newer major", env: "3.0.0", want: "3.0.0"},
		{name: "dev suffix", env: "2.1.400-dev", want: claudeCodeDefaultClientVersion},
		{name: "build metadata", env: "2.1.400+build.1", want: claudeCodeDefaultClientVersion},
		{name: "v prefix", env: "v2.1.400", want: claudeCodeDefaultClientVersion},
		{name: "two parts", env: "2.2", want: claudeCodeDefaultClientVersion},
		{name: "four parts", env: "2.1.400.1", want: claudeCodeDefaultClientVersion},
		{name: "leading zero", env: "2.1.0400", want: claudeCodeDefaultClientVersion},
		{name: "signed part", env: "2.1.+400", want: claudeCodeDefaultClientVersion},
		{name: "older than built-in", env: "2.1.258", want: claudeCodeDefaultClientVersion},
		{name: "older major", env: "1.9.999", want: claudeCodeDefaultClientVersion},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, resolveClaudeCodeClientVersion(tc.env))
		})
	}
}
