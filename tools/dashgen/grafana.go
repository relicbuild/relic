package main

type datasourceRef struct {
	Type string `json:"type"`
	UID  string `json:"uid"`
}

var (
	prometheus = datasourceRef{
		Type: "prometheus",
		UID:  "${datasource}",
	}
	loki = datasourceRef{
		Type: "loki",
		UID:  "${loki}",
	}
)

type target struct {
	RefID        string         `json:"refId"`
	Datasource   *datasourceRef `json:"datasource,omitempty"`
	Expr         string         `json:"expr"`
	LegendFormat string         `json:"legendFormat,omitempty"`
	Exemplar     bool           `json:"exemplar,omitempty"`
	Instant      bool           `json:"instant,omitempty"`
}

type gridPos struct {
	H int `json:"h"`
	W int `json:"w"`
	X int `json:"x"`
	Y int `json:"y"`
}

type fieldConfig struct {
	Defaults  fieldDefaults `json:"defaults"`
	Overrides []any         `json:"overrides"`
}

type fieldDefaults struct {
	Unit     string `json:"unit,omitempty"`
	Min      *int   `json:"min,omitempty"`
	Decimals *int   `json:"decimals,omitempty"`
}

type panel struct {
	ID          int            `json:"id"`
	Type        string         `json:"type"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Datasource  *datasourceRef `json:"datasource,omitempty"`
	GridPos     gridPos        `json:"gridPos"`
	FieldConfig *fieldConfig   `json:"fieldConfig,omitempty"`
	Options     any            `json:"options,omitempty"`
	Targets     []target       `json:"targets,omitempty"`
	Collapsed   *bool          `json:"collapsed,omitempty"`
	Panels      []panel        `json:"panels,omitempty"`
}

type variable struct {
	Name       string         `json:"name"`
	Label      string         `json:"label,omitempty"`
	Type       string         `json:"type"`
	Datasource *datasourceRef `json:"datasource,omitempty"`
	Query      any            `json:"query"`
	Definition string         `json:"definition,omitempty"`
	IncludeAll bool           `json:"includeAll,omitempty"`
	Multi      bool           `json:"multi,omitempty"`
	AllValue   string         `json:"allValue,omitempty"`
	Refresh    int            `json:"refresh,omitempty"`
	Current    map[string]any `json:"current"`
	Hide       int            `json:"hide"`
	Sort       int            `json:"sort,omitempty"`
}

type link struct {
	Title       string   `json:"title"`
	Type        string   `json:"type"`
	Tags        []string `json:"tags,omitempty"`
	URL         string   `json:"url,omitempty"`
	AsDropdown  bool     `json:"asDropdown"`
	IncludeVars bool     `json:"includeVars"`
	KeepTime    bool     `json:"keepTime"`
	TargetBlank bool     `json:"targetBlank"`
}

type dashboard struct {
	UID           string   `json:"uid"`
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	Tags          []string `json:"tags"`
	Editable      bool     `json:"editable"`
	GraphTooltip  int      `json:"graphTooltip"`
	Refresh       string   `json:"refresh"`
	SchemaVersion int      `json:"schemaVersion"`
	Version       int      `json:"version"`
	Time          struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"time"`
	Timezone   string `json:"timezone"`
	Templating struct {
		List []variable `json:"list"`
	} `json:"templating"`
	Links  []link  `json:"links"`
	Panels []panel `json:"panels"`
}
