package identity

// UsersConfig is the top-level type for the users.yaml file used by StaticProvider.
type UsersConfig struct {
	Version         string           `yaml:"version"`
	Users           []UserEntry      `yaml:"users"`
	ServiceAccounts []ServiceAccount `yaml:"service_accounts"`
}

// UserEntry defines a human user and their roles.
type UserEntry struct {
	ID    string   `yaml:"id"`    // e.g., alice@example.com
	Roles []string `yaml:"roles"` // e.g., [dba, sre]
}

// ServiceAccount defines an automated service account.
type ServiceAccount struct {
	ID         string   `yaml:"id"`           // e.g., srebot
	Roles      []string `yaml:"roles"`        // e.g., [sre-automation]
	APIKeyHash string   `yaml:"api_key_hash"` // Argon2id hash of the API key
}
