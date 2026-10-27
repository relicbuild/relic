package repository

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestRepositoryCloneSharesNothingMutable(t *testing.T) {
	t.Parallel()

	period := int64(60)
	original := Repository{
		Name: "everything",
		Config: Config{
			Environments: []string{
				"DEV",
			},
			Architectures: []string{
				"amd64",
			},
			Remote: &RemoteConfig{
				URL:                               "https://upstream.example.com",
				MissedRetrievalCachePeriodSeconds: &period,
			},
			Virtual: &VirtualConfig{
				Members: []string{
					"member",
				},
				LocalOnlyPatterns: []string{
					"com.example.**",
				},
			},
			Extensions: map[string]json.RawMessage{
				"vendor": json.RawMessage(`{"a":1}`),
			},
		},
	}

	want := original.clone()
	copied := original.clone()

	copied.Config.Environments[0] = "changed"
	copied.Config.Architectures[0] = "changed"
	copied.Config.Remote.URL = "changed"
	*copied.Config.Remote.MissedRetrievalCachePeriodSeconds = 0
	copied.Config.Virtual.Members[0] = "changed"
	copied.Config.Virtual.LocalOnlyPatterns[0] = "changed"
	copied.Config.Extensions["vendor"][1] = 'X'
	copied.Config.Extensions["added"] = nil

	if !reflect.DeepEqual(original, want) {
		t.Errorf("original after changing its clone = %+v, want %+v", original, want)
	}
}
