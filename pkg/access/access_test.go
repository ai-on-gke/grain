package access_test

import (
	"testing"

	"github.com/bwsalmon/grain/pkg/access"
)

func TestNormalizeAndValidateIdentifier(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		explicitType access.AccountType
		wantIdent    string
		wantType     access.AccountType
		wantErr      bool
	}{
		{
			name:      "bare user email",
			raw:       "alice@example.com",
			wantIdent: "user:alice@example.com",
			wantType:  access.TypeUser,
		},
		{
			name:      "prefixed user email",
			raw:       "user:bob@example.com",
			wantIdent: "user:bob@example.com",
			wantType:  access.TypeUser,
		},
		{
			name:      "gcp service account inferred",
			raw:       "node-1@project.iam.gserviceaccount.com",
			wantIdent: "serviceAccount:node-1@project.iam.gserviceaccount.com",
			wantType:  access.TypeServiceAccount,
		},
		{
			name:      "prefixed service account",
			raw:       "serviceAccount:ci-runner@prod.iam.gserviceaccount.com",
			wantIdent: "serviceAccount:ci-runner@prod.iam.gserviceaccount.com",
			wantType:  access.TypeServiceAccount,
		},
		{
			name:      "prefixed group",
			raw:       "group:devs@example.com",
			wantIdent: "group:devs@example.com",
			wantType:  access.TypeGroup,
		},
		{
			name:         "explicit type matches",
			raw:          "charlie@example.com",
			explicitType: access.TypeUser,
			wantIdent:    "user:charlie@example.com",
			wantType:     access.TypeUser,
		},
		{
			name:         "explicit type synonym users with bare email",
			raw:          "charlie@example.com",
			explicitType: "users",
			wantIdent:    "user:charlie@example.com",
			wantType:     access.TypeUser,
		},
		{
			name:         "explicit type synonym users with prefixed email",
			raw:          "user:charlie@example.com",
			explicitType: "users",
			wantIdent:    "user:charlie@example.com",
			wantType:     access.TypeUser,
		},
		{
			name:         "explicit type synonym service_accounts with prefix",
			raw:          "serviceAccount:sa@proj.iam.gserviceaccount.com",
			explicitType: "service_accounts",
			wantIdent:    "serviceAccount:sa@proj.iam.gserviceaccount.com",
			wantType:     access.TypeServiceAccount,
		},
		{
			name:         "explicit type synonym groups with prefix",
			raw:          "group:devs@example.com",
			explicitType: "groups",
			wantIdent:    "group:devs@example.com",
			wantType:     access.TypeGroup,
		},
		{
			name:         "invalid explicit type with prefixed identifier",
			raw:          "user:charlie@example.com",
			explicitType: "invalid-type",
			wantErr:      true,
		},
		{
			name:         "explicit type mismatch with prefix",
			raw:          "user:charlie@example.com",
			explicitType: access.TypeServiceAccount,
			wantErr:      true,
		},
		{
			name:    "empty string",
			raw:     "   ",
			wantErr: true,
		},
		{
			name:    "invalid email",
			raw:     "user:invalid @ address",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotIdent, gotType, err := access.ParseAndNormalizeIdentifier(tc.raw, tc.explicitType)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseAndNormalizeIdentifier(%q, %q) err = %v, wantErr = %v", tc.raw, tc.explicitType, err, tc.wantErr)
			}
			if !tc.wantErr {
				if gotIdent != tc.wantIdent {
					t.Errorf("got identifier %q, want %q", gotIdent, tc.wantIdent)
				}
				if gotType != tc.wantType {
					t.Errorf("got type %q, want %q", gotType, tc.wantType)
				}
			}
		})
	}
}

func TestNormalizeRejectsDisplayNameAddresses(t *testing.T) {
	for _, raw := range []string{
		"Alice <alice@example.com>",
		"user:Alice <alice@example.com>",
		"<alice@example.com>",
	} {
		if got, _, err := access.ParseAndNormalizeIdentifier(raw, ""); err == nil {
			t.Errorf("ParseAndNormalizeIdentifier(%q) = %q, want an error: IAM takes bare addresses only", raw, got)
		}
	}
	if got, _, err := access.ParseAndNormalizeIdentifier("alice@example.com", ""); err != nil || got != "user:alice@example.com" {
		t.Errorf("bare address = %q, %v; want user:alice@example.com", got, err)
	}
}
