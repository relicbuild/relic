package main

import "strings"

const sel = `namespace=~"$namespace", instance=~"$instance"`

func expr(e string) string {
	e = strings.ReplaceAll(e, "{S}", "{"+sel+"}")
	return strings.ReplaceAll(e, "{S,", "{"+sel+",")
}

func q(e, legend string) target {
	return target{
		Datasource:   &prometheus,
		Expr:         expr(e),
		LegendFormat: legend,
	}
}

func qx(e, legend string) target {
	t := q(e, legend)
	t.Exemplar = true

	return t
}

func zero() *int { z := 0; return &z }

func timeseries(title, unit string, targets ...target) panel {
	return panel{
		Type:       "timeseries",
		Title:      title,
		Datasource: &prometheus,
		FieldConfig: &fieldConfig{
			Defaults: fieldDefaults{
				Unit: unit,
				Min:  zero(),
			},
			Overrides: []any{},
		},
		Options: map[string]any{
			"legend": map[string]any{
				"displayMode": "list",
				"placement":   "bottom",
				"showLegend":  true,
			},
			"tooltip": map[string]any{
				"mode": "multi",
				"sort": "desc",
			},
		},
		Targets: targets,
	}
}

func stat(title, unit string, targets ...target) panel {
	for i := range targets {
		targets[i].Instant = true
	}

	return panel{
		Type:       "stat",
		Title:      title,
		Datasource: &prometheus,
		FieldConfig: &fieldConfig{
			Defaults: fieldDefaults{
				Unit: unit,
			},
			Overrides: []any{},
		},
		Options: map[string]any{
			"reduceOptions": map[string]any{
				"calcs": []string{
					"lastNotNull",
				},
				"fields": "",
				"values": false,
			},
			"colorMode": "value",
			"graphMode": "none",
			"textMode":  "auto",
		},
		Targets: targets,
	}
}

func bargauge(title, unit string, targets ...target) panel {
	for i := range targets {
		targets[i].Instant = true
	}

	return panel{
		Type:       "bargauge",
		Title:      title,
		Datasource: &prometheus,
		FieldConfig: &fieldConfig{
			Defaults: fieldDefaults{
				Unit: unit,
				Min:  zero(),
			},
			Overrides: []any{},
		},
		Options: map[string]any{
			"reduceOptions": map[string]any{
				"calcs": []string{
					"lastNotNull",
				},
				"fields": "",
				"values": false,
			},
			"orientation": "horizontal",
			"displayMode": "gradient",
		},
		Targets: targets,
	}
}

func logs(title, query string) panel {
	return panel{
		Type:       "logs",
		Title:      title,
		Datasource: &loki,
		Options: map[string]any{
			"showTime":         true,
			"wrapLogMessage":   true,
			"enableLogDetails": true,
			"sortOrder":        "Descending",
		},
		Targets: []target{
			{
				RefID:      "A",
				Datasource: &loki,
				Expr:       query,
			},
		},
	}
}

func description(p panel, d string) panel {
	p.Description = d
	return p
}

type builder struct {
	d       dashboard
	id      int
	x, y, h int
}

func newDashboard(uid, title, desc string, withLoki bool) *builder {
	b := &builder{}
	d := &b.d
	d.UID, d.Title, d.Description = uid, title, desc
	d.Tags = []string{
		"relic",
	}
	d.Editable, d.GraphTooltip, d.Refresh = true, 1, "30s"
	d.SchemaVersion, d.Version = 39, 1
	d.Time.From, d.Time.To = "now-6h", "now"
	d.Timezone = "browser"
	all := map[string]any{
		"selected": true,
		"text": []string{
			"All",
		},
		"value": []string{
			"$__all",
		},
	}

	d.Templating.List = []variable{
		{
			Name:    "datasource",
			Label:   "Prometheus",
			Type:    "datasource",
			Query:   "prometheus",
			Current: map[string]any{},
		},
	}
	if withLoki {
		d.Templating.List = append(d.Templating.List,
			variable{
				Name:    "loki",
				Label:   "Loki",
				Type:    "datasource",
				Query:   "loki",
				Current: map[string]any{},
			})
	}

	d.Templating.List = append(d.Templating.List,
		variable{
			Name:       "namespace",
			Type:       "query",
			Datasource: &prometheus,
			IncludeAll: true,
			Multi:      true,
			AllValue:   ".*",
			Refresh:    2,
			Sort:       1,
			Query: map[string]any{
				"query": "label_values(relic_build_info, namespace)",
				"refId": "namespace",
			},
			Definition: "label_values(relic_build_info, namespace)",
			Current:    all,
		},
		variable{
			Name:       "instance",
			Type:       "query",
			Datasource: &prometheus,
			IncludeAll: true,
			Multi:      true,
			Refresh:    2,
			Sort:       1,
			Query: map[string]any{
				"query": `label_values(relic_build_info{namespace=~"$namespace"}, instance)`,
				"refId": "instance",
			},
			Definition: `label_values(relic_build_info{namespace=~"$namespace"}, instance)`,
			Current:    all,
		},
	)
	d.Links = []link{
		{
			Title: "Relic dashboards",
			Type:  "dashboards",
			Tags: []string{
				"relic",
			},
			AsDropdown:  true,
			IncludeVars: true,
			KeepTime:    true,
		},
	}
	d.Panels = []panel{}

	return b
}

func (b *builder) row(title string) {
	b.newline()
	b.id++
	collapsed := false
	b.d.Panels = append(
		b.d.Panels,
		panel{
			ID:    b.id,
			Type:  "row",
			Title: title,
			GridPos: gridPos{
				H: 1,
				W: 24,
				Y: b.y,
			},
			Collapsed: &collapsed,
			Panels:    []panel{},
		},
	)
	b.y++
}

func (b *builder) newline() {
	if b.x > 0 {
		b.x, b.y, b.h = 0, b.y+b.h, 0
	}
}

func (b *builder) add(w, h int, p panel) {
	if b.x+w > 24 {
		b.newline()
	}

	b.id++
	p.ID = b.id

	p.GridPos = gridPos{
		H: h,
		W: w,
		X: b.x,
		Y: b.y,
	}
	for i := range p.Targets {
		p.Targets[i].RefID = string(rune('A' + i))
	}

	b.d.Panels = append(b.d.Panels, p)
	b.x += w
	b.h = max(b.h, h)
}

func (b *builder) link(title, url string) {
	b.d.Links = append(b.d.Links, link{
		Title:       title,
		Type:        "link",
		URL:         url,
		IncludeVars: false,
		KeepTime:    true,
	})
}
