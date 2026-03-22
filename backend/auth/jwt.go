package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Claims struct {
	UserID      int64  `json:"user_id"`
	OrgID       int64  `json:"org_id"`
	Role        string `json:"role"`
	Fingerprint string `json:"fpr"` // SHA-256 of IP + User-Agent
	jwt.RegisteredClaims
}

func CreateAccessToken(userID, orgID int64, role, secret, fingerprint string, ttl time.Duration) (string, error) {
	claims := Claims{
		UserID:      userID,
		OrgID:       orgID,
		Role:        role,
		Fingerprint: fingerprint,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Subject:   fmt.Sprintf("%d", userID),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

func CreateTokenPair(userID, orgID int64, role, secret, fingerprint string, accessTTL, refreshTTL time.Duration) (accessToken, refreshToken string, err error) {
	accessToken, err = CreateAccessToken(userID, orgID, role, secret, fingerprint, accessTTL)
	if err != nil {
		return "", "", fmt.Errorf("failed to create access token: %w", err)
	}

	// Refresh token is a random string, not a JWT
	refreshToken, err = GenerateRefreshToken()
	if err != nil {
		return "", "", fmt.Errorf("failed to create refresh token: %w", err)
	}

	return accessToken, refreshToken, nil
}

func ValidateToken(tokenString, secret string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	return claims, nil
}
