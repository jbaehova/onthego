package cli

import "github.com/jbaehova/onthego/internal/identity"

func identitySummary() ([2]string, error) {
	keys, err := identity.LoadOrCreate()
	if err != nil {
		return [2]string{}, err
	}
	return [2]string{keys.AgeRecipient, keys.SigningPublic}, nil
}
