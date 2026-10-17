package scim

import (
	"net/http"
)

type supported struct {
	Supported bool `json:"supported"`
}

type filterSupport struct {
	Supported  bool `json:"supported"`
	MaxResults int  `json:"maxResults"`
}

type bulkSupport struct {
	Supported      bool `json:"supported"`
	MaxOperations  int  `json:"maxOperations"`
	MaxPayloadSize int  `json:"maxPayloadSize"`
}

type authenticationScheme struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Primary     bool   `json:"primary"`
}

type serviceProviderConfigResource struct {
	Schemas               []string               `json:"schemas"`
	Patch                 supported              `json:"patch"`
	Bulk                  bulkSupport            `json:"bulk"`
	Filter                filterSupport          `json:"filter"`
	ChangePassword        supported              `json:"changePassword"`
	Sort                  supported              `json:"sort"`
	Etag                  supported              `json:"etag"`
	AuthenticationSchemes []authenticationScheme `json:"authenticationSchemes"`
}

func serviceProviderConfig(w http.ResponseWriter, _ *http.Request) {
	write(w, http.StatusOK, serviceProviderConfigResource{
		Schemas: []string{
			"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig",
		},
		Patch: supported{
			Supported: true,
		},
		Filter: filterSupport{
			Supported:  true,
			MaxResults: maxResults,
		},
		AuthenticationSchemes: []authenticationScheme{
			{
				Type:        "oauthbearertoken",
				Name:        "Bearer token",
				Description: "A Relic access token of an administrator",
				Primary:     true,
			},
		},
	})
}

type resourceType struct {
	Schemas  []string `json:"schemas"`
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Endpoint string   `json:"endpoint"`
	Schema   string   `json:"schema"`
}

func resourceTypes(w http.ResponseWriter, _ *http.Request) {
	types := []any{
		resourceType{
			Schemas: []string{
				"urn:ietf:params:scim:schemas:core:2.0:ResourceType",
			},
			ID:       "User",
			Name:     "User",
			Endpoint: "/Users",
			Schema:   userSchema,
		},
		resourceType{
			Schemas: []string{
				"urn:ietf:params:scim:schemas:core:2.0:ResourceType",
			},
			ID:       "Group",
			Name:     "Group",
			Endpoint: "/Groups",
			Schema:   groupSchema,
		},
	}
	write(w, http.StatusOK, list(int64(len(types)), 1, types))
}

type attribute struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	MultiValued bool   `json:"multiValued"`
	Required    bool   `json:"required"`
	Mutability  string `json:"mutability"`
	Uniqueness  string `json:"uniqueness,omitempty"`
}

type schema struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	Attributes []attribute `json:"attributes"`
}

func schemas(w http.ResponseWriter, _ *http.Request) {
	all := []any{
		schema{
			ID:   userSchema,
			Name: "User",
			Attributes: []attribute{
				{
					Name:       "userName",
					Type:       "string",
					Required:   true,
					Mutability: "immutable",
					Uniqueness: "server",
				},
				{
					Name:       "active",
					Type:       "boolean",
					Mutability: "readWrite",
				},
				{
					Name:        "emails",
					Type:        "complex",
					MultiValued: true,
					Mutability:  "readWrite",
				},
			},
		},
		schema{
			ID:   groupSchema,
			Name: "Group",
			Attributes: []attribute{
				{
					Name:       "displayName",
					Type:       "string",
					Required:   true,
					Mutability: "immutable",
					Uniqueness: "server",
				},
				{
					Name:        "members",
					Type:        "complex",
					MultiValued: true,
					Mutability:  "readWrite",
				},
			},
		},
	}
	write(w, http.StatusOK, list(int64(len(all)), 1, all))
}
