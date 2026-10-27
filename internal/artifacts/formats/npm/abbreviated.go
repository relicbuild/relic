package npm

// AbbreviatedMediaType is the media type of npm's abbreviated packument,
// the install metadata npm clients ask for.
const AbbreviatedMediaType = "application/vnd.npm.install-v1+json"

// Form is the shape of a served packument.
type Form int

// The forms of a packument.
const (
	FormFull Form = iota
	FormAbbreviated
)

var abbreviatedVersionFields = map[string]bool{
	"name":                 true,
	"version":              true,
	"deprecated":           true,
	"dependencies":         true,
	"acceptDependencies":   true,
	"optionalDependencies": true,
	"devDependencies":      true,
	"bundleDependencies":   true,
	"peerDependencies":     true,
	"peerDependenciesMeta": true,
	"bin":                  true,
	"directories":          true,
	"dist":                 true,
	"engines":              true,
	"_hasShrinkwrap":       true,
	"hasInstallScript":     true,
	"funding":              true,
	"cpu":                  true,
	"os":                   true,
}

func abbreviate(doc Packument) Packument {
	out := Packument{}

	for _, key := range []string{
		"name",
		"dist-tags",
	} {
		if v, ok := doc[key]; ok {
			out[key] = v
		}
	}

	if times, ok := doc["time"].(map[string]any); ok {
		if modified, ok := times["modified"]; ok {
			out["modified"] = modified
		}
	}

	versions, ok := doc["versions"].(map[string]any)
	if !ok {
		return out
	}

	abbreviated := make(map[string]any, len(versions))

	for version, v := range versions {
		manifest, ok := v.(map[string]any)
		if !ok {
			abbreviated[version] = v
			continue
		}

		kept := map[string]any{}

		for key, value := range manifest {
			if abbreviatedVersionFields[key] {
				kept[key] = value
			}
		}

		abbreviated[version] = kept
	}

	out["versions"] = abbreviated

	return out
}
