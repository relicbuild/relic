package scan

import "testing"

func TestDependencyPURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, moduleType, id, want string
	}{
		{
			name:       "maven",
			moduleType: "maven",
			id:         "org.apache.commons:commons-lang3:3.12.0",
			want:       "pkg:maven/org.apache.commons/commons-lang3@3.12.0",
		},
		{
			name:       "gradle is maven",
			moduleType: "Gradle",
			id:         "com.google.guava:guava:31.1-jre",
			want:       "pkg:maven/com.google.guava/guava@31.1-jre",
		},
		{
			name:       "npm",
			moduleType: "npm",
			id:         "lodash:4.17.20",
			want:       "pkg:npm/lodash@4.17.20",
		},
		{
			name:       "scoped npm",
			moduleType: "npm",
			id:         "@babel/core:7.20.0",
			want:       "pkg:npm/%40babel/core@7.20.0",
		},
		{
			name:       "go",
			moduleType: "golang",
			id:         "golang.org/x/text:v0.3.7",
			want:       "pkg:golang/golang.org/x/text@v0.3.7",
		},
		{
			name:       "python, normalized",
			moduleType: "python",
			id:         "Django_Rest.Framework:3.14.0",
			want:       "pkg:pypi/django-rest-framework@3.14.0",
		},
		{
			name:       "python, repeated separators collapse",
			moduleType: "python",
			id:         "Zope__Interface._-Extras:1.0",
			want:       "pkg:pypi/zope-interface-extras@1.0",
		},
		{
			name:       "nuget",
			moduleType: "dotnet",
			id:         "Newtonsoft.Json:13.0.1",
			want:       "pkg:nuget/Newtonsoft.Json@13.0.1",
		},
		{
			name:       "maven without a group",
			moduleType: "maven",
			id:         "commons-lang3:3.12.0",
		},
		{
			name:       "maven with more than group and artifact",
			moduleType: "maven",
			id:         "org.example:lib:extra:1.0",
		},
		{
			name:       "no version",
			moduleType: "npm",
			id:         "lodash",
		},
		{
			name:       "empty version",
			moduleType: "npm",
			id:         "lodash:",
		},
		{
			name:       "unknown type",
			moduleType: "docker",
			id:         "alpine:3.19",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := DependencyPURL(tt.moduleType, tt.id)
			if got != tt.want || ok != (tt.want != "") {
				t.Errorf("DependencyPURL(%q, %q) = %q, %v; want %q", tt.moduleType, tt.id, got, ok, tt.want)
			}
		})
	}
}
