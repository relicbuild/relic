package repository

import "regexp"

// PropertyKeyRule says in words what ValidPropertyKey accepts.
const PropertyKeyRule = "a property key is 1 to 255 characters without spaces, '=', ';', ',' or '|'"

var propertyKey = regexp.MustCompile(`^[^\s=;,|]{1,255}$`)

// ValidPropertyKey reports whether k may name a property.
func ValidPropertyKey(k string) bool { return propertyKey.MatchString(k) }
