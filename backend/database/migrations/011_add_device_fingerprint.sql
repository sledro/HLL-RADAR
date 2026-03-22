-- Add device fingerprint to refresh tokens for device binding
ALTER TABLE refresh_tokens ADD COLUMN IF NOT EXISTS fingerprint TEXT NOT NULL DEFAULT '';
