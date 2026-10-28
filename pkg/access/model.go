package access

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
)

// AccountType identifies the kind of identity authorized for access.
type AccountType string

const (
	TypeUser           AccountType = "user"
	TypeServiceAccount AccountType = "service_account"
	TypeGroup          AccountType = "group"
)

// ParseAndNormalizeIdentifier inspects an input identity string and optional account type,
// normalizing it into the standard format (e.g. "user:alice@example.com", "serviceAccount:node-1@...", "group:...").
func ParseAndNormalizeIdentifier(raw string, explicitType AccountType) (string, AccountType, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", errors.New("identifier cannot be empty")
	}

	var expectedType AccountType
	if explicitType != "" {
		norm, err := normalizeAccountType(explicitType)
		if err != nil {
			return "", "", err
		}
		expectedType = norm
	}

	var accType AccountType
	var principal string

	switch {
	case strings.HasPrefix(raw, "user:"):
		accType = TypeUser
		principal = strings.TrimPrefix(raw, "user:")
	case strings.HasPrefix(raw, "serviceAccount:"):
		accType = TypeServiceAccount
		principal = strings.TrimPrefix(raw, "serviceAccount:")
	case strings.HasPrefix(raw, "group:"):
		accType = TypeGroup
		principal = strings.TrimPrefix(raw, "group:")
	default:
		principal = raw
		if expectedType != "" {
			accType = expectedType
		} else {
			if strings.HasSuffix(principal, ".gserviceaccount.com") {
				accType = TypeServiceAccount
			} else {
				accType = TypeUser
			}
		}
	}

	principal = strings.TrimSpace(principal)
	if principal == "" {
		return "", "", errors.New("identity address/name cannot be empty")
	}

	// If explicitType was passed and doesn't match parsed type prefix, validate compatibility
	if expectedType != "" && accType != expectedType {
		return "", "", fmt.Errorf("identifier prefix %q does not match specified type %q", raw, explicitType)
	}

	// Validate basic email format if it contains an '@'. mail.ParseAddress
	// also accepts RFC 5322 forms such as "Alice <alice@example.com>", which
	// IAM rejects as a member, so the parsed address must be the whole input.
	if strings.Contains(principal, "@") {
		addr, err := mail.ParseAddress(principal)
		if err != nil {
			return "", "", fmt.Errorf("invalid email/identity address %q: %w", principal, err)
		}
		if addr.Address != principal {
			return "", "", fmt.Errorf("invalid email/identity address %q: want a bare address such as %q", principal, addr.Address)
		}
	}

	var normalized string
	switch accType {
	case TypeUser:
		normalized = "user:" + principal
	case TypeServiceAccount:
		normalized = "serviceAccount:" + principal
	case TypeGroup:
		normalized = "group:" + principal
	default:
		return "", "", fmt.Errorf("unsupported account type %q", accType)
	}

	return normalized, accType, nil
}

func normalizeAccountType(t AccountType) (AccountType, error) {
	switch t {
	case TypeUser, "users":
		return TypeUser, nil
	case TypeServiceAccount, "serviceAccount", "service_accounts":
		return TypeServiceAccount, nil
	case TypeGroup, "groups":
		return TypeGroup, nil
	default:
		return "", fmt.Errorf("invalid account type %q (must be user, service_account, or group)", t)
	}
}
