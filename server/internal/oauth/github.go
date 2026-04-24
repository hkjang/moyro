package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GitHubProvider handles GitHub's OAuth Apps flow. We request `read:user`
// and `user:email`; the latter is the only way to retrieve a verified
// primary email when the user has made it private on their profile.
type GitHubProvider struct {
	ClientID     string
	ClientSecret string
}

func (*GitHubProvider) Name() string { return "github" }

func (p *GitHubProvider) AuthURL(state, redirectURL string) string {
	q := url.Values{}
	q.Set("client_id", p.ClientID)
	q.Set("redirect_uri", redirectURL)
	q.Set("scope", "read:user user:email")
	q.Set("state", state)
	q.Set("allow_signup", "true")
	return "https://github.com/login/oauth/authorize?" + q.Encode()
}

type ghTokenResp struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	Error       string `json:"error"`
}

type ghUserResp struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatar_url"`
}

type ghEmail struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

func (p *GitHubProvider) Exchange(ctx context.Context, code, redirectURL string) (*UserInfo, error) {
	form := url.Values{}
	form.Set("client_id", p.ClientID)
	form.Set("client_secret", p.ClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURL)

	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://github.com/login/oauth/access_token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// GitHub defaults to form-encoded response; explicitly opt into JSON.
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrExchangeFailed, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%w: token status %d", ErrExchangeFailed, resp.StatusCode)
	}
	var tok ghTokenResp
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return nil, fmt.Errorf("%w: decode token: %v", ErrExchangeFailed, err)
	}
	if tok.Error != "" {
		return nil, fmt.Errorf("%w: %s", ErrExchangeFailed, tok.Error)
	}
	if tok.AccessToken == "" {
		return nil, errors.New("github: empty access_token")
	}

	// Fetch the primary profile.
	uReq, _ := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	uReq.Header.Set("Authorization", "token "+tok.AccessToken)
	uReq.Header.Set("Accept", "application/vnd.github+json")
	uResp, err := client.Do(uReq)
	if err != nil {
		return nil, fmt.Errorf("%w: user: %v", ErrExchangeFailed, err)
	}
	defer uResp.Body.Close()
	if uResp.StatusCode != 200 {
		return nil, fmt.Errorf("%w: user status %d", ErrExchangeFailed, uResp.StatusCode)
	}
	var u ghUserResp
	if err := json.NewDecoder(uResp.Body).Decode(&u); err != nil {
		return nil, fmt.Errorf("%w: decode user: %v", ErrExchangeFailed, err)
	}

	email := u.Email
	verified := email != "" // the /user payload only returns public email; verified flag not exposed there
	// If the user hides their email (common), fetch /user/emails and
	// grab the primary+verified one.
	if email == "" {
		eReq, _ := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user/emails", nil)
		eReq.Header.Set("Authorization", "token "+tok.AccessToken)
		eReq.Header.Set("Accept", "application/vnd.github+json")
		eResp, err := client.Do(eReq)
		if err == nil {
			defer eResp.Body.Close()
			if eResp.StatusCode == 200 {
				var emails []ghEmail
				if err := json.NewDecoder(eResp.Body).Decode(&emails); err == nil {
					for _, e := range emails {
						if e.Primary && e.Verified {
							email = e.Email
							verified = true
							break
						}
					}
				}
			}
		}
	}

	if u.ID == 0 {
		return nil, errors.New("github: missing user id")
	}
	return &UserInfo{
		Subject:       strconv.FormatInt(u.ID, 10),
		Email:         email,
		EmailVerified: verified,
		Name:          firstNonEmpty(u.Name, u.Login),
		Picture:       u.AvatarURL,
	}, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
