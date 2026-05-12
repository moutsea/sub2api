package service

import (
	"context"
	"fmt"
	"log"
)

func resolveKiroProfileArn(ctx context.Context, account *Account, tokenProvider *KiroTokenProvider, accountRepo AccountRepository, prefix string) (string, error) {
	if account == nil || !account.IsKiro() || account.IsKiroApiKey() {
		return "", nil
	}

	profileArn := ""
	if tokenProvider != nil {
		profileArn = tokenProvider.GetProfileArn(account.ID)
	}
	if profileArn == "" {
		profileArn = account.GetKiroProfileArn()
	}
	if profileArn == "" && accountRepo != nil {
		freshAccount, err := accountRepo.GetByID(ctx, account.ID)
		if err == nil && freshAccount != nil {
			profileArn = freshAccount.GetKiroProfileArn()
			if profileArn != "" && prefix != "" {
				log.Printf("%s profile_arn recovered from db (snapshot was stale)", prefix)
			}
		}
	}

	if profileArn == "" && account.IsKiroSSOOIDC() {
		return "", fmt.Errorf("kiro idc account %d is missing profile_arn; IDC refresh responses do not include profileArn, import the matching profile ARN from Kiro profile.json or token JSON", account.ID)
	}

	return profileArn, nil
}
