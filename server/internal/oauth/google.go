package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GoogleProvider handles the OAuth2 Authorization Code flow against Google.
// We ask for the "openid email profile" scope — sufficient to reliably
// obtain the subject (sub), email, verified-email flag, name and picture.
type GoogleProvider struct {
	ClientID     string
	ClientSecret string
}

func (*GoogleProvider) Name() string { return "google" }

func (p *GoogleProvider) AuthURL(state, redirectURL string) string {
	// prompt=select_account forces Google to show the account chooser
	// even when the user is already signed in — avoids silent account
	// binding surprises when a shared browser is in use.
	q := url.Values{}
	q.Set("client_id", p.ClientID)
	q.Set("redirect_uri", redirectURL)
	q.Set("response_type", "code")
	q.Set("scope", "openid email profile")
	q.Set("state", state)
	q.Set("access_type", "online")
	q.Set("prompt", "select_account")
	return "https://accounts.google.com/o/oauth2/v2/auth?" + q.Encode()
}

type googleTokenResp struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	IDToken     string `json:"id_token"`
}

type googleUserResp struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
}

func (p *GoogleProvider) Exchange(ctx context.Context, code, redirectURL string) (*UserInfo, error) {
	form := url.Values{}
	form.Set("code", code)
	form.Set("client_id", p.ClientID)
	form.Set("client_secret", p.ClientSecret)
	form.Set("redirect_uri", redirectURL)
	form.Set("grant_type", "authorization_code")

	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrExchangeFailed, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%w: token status %d", ErrExchangeFailed, resp.StatusCode)
	}
	var tok googleTokenResp
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return nil, fmt.Errorf("%w: decode token: %v", ErrExchangeFailed, err)
	}
	if tok.AccessToken == "" {
		return nil, errors.New("google: empty access_token")
	}

	// Call userinfo rather than decoding the ID token: no JWT-parsing
	// code, and Google returns the same sub/email/name via HTTP anyway.
	uReq, _ := http.NewRequestWithContext(ctx, "GET", "https://openidconnect.googleapis.com/v1/userinfo", nil)
	uReq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	uResp, err := client.Do(uReq)
	if err != nil {
		return nil, fmt.Errorf("%w: userinfo: %v", ErrExchangeFailed, err)
	}
	defer uResp.Body.Close()
	if uResp.StatusCode != 200 {
		return nil, fmt.Errorf("%w: userinfo status %d", ErrExchangeFailed, uResp.StatusCode)
	}
	var u googleUserResp
	if err := json.NewDecoder(uResp.Body).Decode(&u); err != nil {
		return nil, fmt.Errorf("%w: decode userinfo: %v", ErrExchangeFailed, err)
	}
	if u.Sub == "" {
		return nil, errors.New("google: userinfo missing sub")
	}
	return &UserInfo{
		Subject:       u.Sub,
		Email:         u.Email,
		EmailVerified: u.EmailVerified,
		Name:          u.Name,
		Picture:       u.Picture,
	}, nil
}
