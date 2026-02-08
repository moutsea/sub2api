package kiro

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// GenerateMachineID generates a machine ID from a refresh token.
// Uses SHA256("KotlinNativeAPI/{refreshToken}") to produce a 64-char hex string.
// Reference: kiro.rs machine_id.rs
func GenerateMachineID(refreshToken string) string {
	if refreshToken == "" {
		return ""
	}
	hash := sha256.Sum256([]byte(fmt.Sprintf("KotlinNativeAPI/%s", refreshToken)))
	return hex.EncodeToString(hash[:])
}
