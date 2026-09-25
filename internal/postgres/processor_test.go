package postgres

import (
	"errors"
	"testing"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/domain"
)

// TestValidateReplayIdentityRequiresMatchingKeyExternalIDAndPayload cobre os critérios de replay sem precisar de PostgreSQL.
func TestValidateReplayIdentityRequiresMatchingKeyExternalIDAndPayload(t *testing.T) {
	t.Parallel()

	candidate := domain.WagerTransactionSnapshot{
		ExternalTransactionID: "external-1",
		IdempotencyKey:        "provider-a:key-1",
		PayloadHash:           []byte{1, 2, 3},
	}
	tests := []struct {
		name      string
		existing  persistedIdentity
		wantError bool
	}{
		{
			name: "same identity and payload replays",
			existing: persistedIdentity{
				externalID: "external-1", idempotencyKey: "provider-a:key-1", payloadHash: []byte{1, 2, 3},
			},
		},
		{
			name: "same external id with a different key conflicts",
			existing: persistedIdentity{
				externalID: "external-1", idempotencyKey: "provider-a:key-2", payloadHash: []byte{1, 2, 3},
			},
			wantError: true,
		},
		{
			name: "same key with a different external id conflicts",
			existing: persistedIdentity{
				externalID: "external-2", idempotencyKey: "provider-a:key-1", payloadHash: []byte{1, 2, 3},
			},
			wantError: true,
		},
		{
			name: "same identities with a different payload conflict",
			existing: persistedIdentity{
				externalID: "external-1", idempotencyKey: "provider-a:key-1", payloadHash: []byte{1, 2, 4},
			},
			wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateReplayIdentity(candidate, test.existing)
			if test.wantError && !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("validateReplayIdentity() error = %v, want idempotency conflict", err)
			}
			if !test.wantError && err != nil {
				t.Fatalf("validateReplayIdentity() error = %v", err)
			}
		})
	}
}
