package sshsetup

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestCheckAgent(t *testing.T) {
	for _, c := range []struct {
		code int
		err  error
		want []Finding
	}{
		{0, nil, nil},                          // keys listed
		{1, nil, nil},                          // agent running, no keys yet: AddKeysToAgent adds one
		{2, nil, []Finding{{Note, AgentNote}}}, // cannot reach the agent
		{0, errors.New("ssh-add not found"), nil},
	} {
		got := CheckAgent(context.Background(), func(context.Context) (int, error) { return c.code, c.err })
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("code %d err %v: got %+v", c.code, c.err, got)
		}
	}
}
