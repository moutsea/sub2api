package kiro

import (
	crypto_rand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"time"
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

// GenerateRandomMachineID generates a random 64-char hex string that matches
// the format of a real SHA256 machine ID. Used for Free-tier device fingerprint rotation.
func GenerateRandomMachineID() string {
	b := make([]byte, 32)
	if _, err := crypto_rand.Read(b); err != nil {
		// Fallback: deterministic from current timestamp (still unique per call)
		// crypto/rand failure indicates a serious OS-level RNG issue — log loudly
		log.Printf("[WARN] crypto/rand.Read failed: %v, falling back to timestamp-based machine ID", err)
		hash := sha256.Sum256([]byte(fmt.Sprintf("KotlinNativeAPI/random/%d", time.Now().UnixNano())))
		return hex.EncodeToString(hash[:])
	}
	return hex.EncodeToString(b)
}
