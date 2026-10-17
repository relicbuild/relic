package huggingface

import "testing"

func TestUpstreamURLs(t *testing.T) {
	t.Parallel()

	const sha = "0123456789abcdef0123456789abcdef01234567"

	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			"revision",
			upstreamRevisionURL("https://hf.example", "models", "org/model", "main"),
			"https://hf.example/api/models/org/model/revision/main",
		},
		{
			"revision with a slash, a query and a fragment",
			upstreamRevisionURL("https://hf.example", "models", "org/model", "refs/pr/1?x=y#z"),
			"https://hf.example/api/models/org/model/revision/refs%2Fpr%2F1%3Fx=y%23z",
		},
		{
			"model file",
			upstreamFileURL("https://hf.example", "models", "org/model", sha, "sub/config.json"),
			"https://hf.example/org/model/resolve/" + sha + "/sub/config.json",
		},
		{
			"dataset file with a query and a fragment",
			upstreamFileURL("https://hf.example", "datasets", "org/data", sha, "a?b#c.txt"),
			"https://hf.example/datasets/org/data/resolve/" + sha + "/a%3Fb%23c.txt",
		},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
		}
	}
}
