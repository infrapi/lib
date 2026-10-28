package auth

import "encoding/json"

// UserInfo is the identity the authentication service resolves a bearer
// token to, from its POST /oauth2/userinfo response: the fixed
// sub/provider/authorization/groups members plus whatever directory
// attributes the authorization backend carries (email, name, employee_id,
// ...), which the service flattens into the same JSON object rather than
// nesting them.
type UserInfo struct {
	// Subject is the user id the service resolved the token to ("sub").
	Subject string
	// Provider is the authentifications.<name> entry whose key verified
	// the token's signature.
	Provider string
	// Authorization is the authorizations.<name> backend (ldap, dev, ...)
	// that resolved Subject's groups and attributes.
	Authorization string
	// Groups is every group Subject is a member of, per that backend.
	Groups []string
	// Attributes are the directory fields the backend's user_attributes
	// mapping exports (email, name, employee_id, ...).
	Attributes map[string]string
}

// UnmarshalJSON splits the fixed members of the service's response from
// the attributes flattened alongside them.
func (u *UserInfo) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	*u = UserInfo{Attributes: map[string]string{}}

	if v, ok := raw["sub"].(string); ok {
		u.Subject = v
	}
	if v, ok := raw["provider"].(string); ok {
		u.Provider = v
	}
	if v, ok := raw["authorization"].(string); ok {
		u.Authorization = v
	}
	if v, ok := raw["groups"].([]any); ok {
		for _, g := range v {
			if s, ok := g.(string); ok {
				u.Groups = append(u.Groups, s)
			}
		}
	}

	for k, v := range raw {
		switch k {
		case "sub", "provider", "authorization", "groups":
			continue
		}
		if s, ok := v.(string); ok {
			u.Attributes[k] = s
		}
	}

	return nil
}
